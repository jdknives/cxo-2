package node

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/SkycoinPro/cxo-2-node/pkg/errors"

	"github.com/SkycoinPro/cxo-2-node/pkg/config"
	"github.com/SkycoinPro/cxo-2-node/pkg/model"
	"github.com/SkycoinPro/cxo-2-node/pkg/node/data"
	dmsghttp "github.com/SkycoinProject/dmsg-http"
	"github.com/SkycoinProject/dmsg/cipher"
	log "github.com/sirupsen/logrus"
)

// Service - node service model
type Service struct {
	config config.Config
	db     data.Data
}

// NewService - initialize node service
func NewService(cfg config.Config) *Service {
	return &Service{
		config: cfg,
		db:     data.DefaultData(),
	}
}

var notifyRoute = "/notify"

// Run - start's node service
func (s *Service) Run() {
	httpS := dmsghttp.Server{
		PubKey:    s.config.PubKey,
		SecKey:    s.config.SecKey,
		Port:      s.config.Port,
		Discovery: s.config.Discovery,
	}

	log.Infof("Starting cxo node with public key: %s and port: %v", s.config.PubKey.Hex(), s.config.Port)

	// prepare server route handling
	mux := http.NewServeMux()
	mux.HandleFunc(notifyRoute, s.notifyHandler)

	// run the server
	sErr := make(chan error, 1)
	sErr <- httpS.Serve(mux)
	close(sErr)
}

func (s *Service) notifyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var rootHash model.RootHash
	err := json.NewDecoder(r.Body).Decode(&rootHash)
	if err != nil {
		log.Error("Error while receiving new root hash: ", err)
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	fmt.Println("Received new root hash from cxo tracker service: ", rootHash.Key())

	go func() {
		time.Sleep(3 * time.Second)
		s.requestData(rootHash)
	}()

	w.WriteHeader(http.StatusOK)
}

func (s *Service) requestData(rootHash model.RootHash) {
	_, err := s.db.GetRootHash(rootHash.Key())
	if err == nil {
		fmt.Printf("received root hash with key: %v already exist", rootHash.Key())
		return
	}
	if err != errors.ErrCannotFindRootHash {
		fmt.Print(err.Error())
		return
	}

	if err := s.db.SaveRootHash(rootHash); err != nil {
		fmt.Printf("saving root hash with key: %v failed due to error: %v", rootHash.Key(), err)
		return
	}

	sPK, sSK := cipher.GenerateKeyPair()
	client := dmsghttp.DMSGClient(s.config.Discovery, sPK, sSK)

	if err := s.retrieveHeaders(client, rootHash, rootHash.ObjectHeaderHash); err != nil {
		fmt.Printf("retrieveing headers failed due to error: %v", err)
		return
	}

	newObjectHeaderHashes, err := s.db.FindNewObjectHeaderHashes(rootHash.Key(), rootHash.Timestamp)
	if err != nil {
		fmt.Printf("fetching new headers failed due to error: %v", err)
		return
	}

	path := s.createStoragePathForPublisher(rootHash.Publisher)
	s.storeHeaderOnPath(rootHash.ObjectHeaderHash, path, newObjectHeaderHashes, client)
	s.removeUnreferencedFiles(rootHash.Key())

	fmt.Println("Update of local storage finished successfully")
}

func (s *Service) createStoragePathForPublisher(publisher string) string {
	publisherStoragePath := filepath.Join(s.config.StoragePath, publisher)
	if _, err := os.Stat(publisherStoragePath); os.IsNotExist(err) {
		if errDir := os.Mkdir(publisherStoragePath, os.ModePerm); errDir != nil {
			fmt.Printf("unable to prepare storage directory: %v due to err: %v", publisherStoragePath, err)
			panic(err)
		}
	}

	return publisherStoragePath
}

func (s *Service) storeHeaderOnPath(headerHash, path string, newHeaders map[string]struct{}, client *http.Client) {
	header, err := s.db.GetObjectHeader(headerHash)
	name := name(header)
	if err != nil {
		fmt.Printf("Unable to store header %s due to error %v", headerHash, err)
		return
	}
	if isDirectory(header) {
		if _, contains := newHeaders[headerHash]; !contains {
			return
		}

		path = filepath.Join(path, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = os.Mkdir(path, os.ModePerm)
		}
		err = s.db.SaveObjectInfo(headerHash, path)
		if err != nil {
			fmt.Printf("saving object in db with hash: %v failed with error: %v", headerHash, err)
		}

		for _, ref := range header.ExternalReferences {
			s.storeHeaderOnPath(ref, path, newHeaders, client)
		}

		return
	}

	if _, contains := newHeaders[headerHash]; !contains {
		return
	}

	filePath := filepath.Join(path, name)
	object, err := s.fetchObject(client, header.ObjectHash)
	if err != nil {
		fmt.Printf("error writing file to local storage - can't fetch content for file %s", name)
	} else {
		err = s.db.SaveObjectInfo(header.ObjectHash, filePath)
		if err != nil {
			fmt.Printf("saving object in db with hash: %v failed with error: %v", header.ObjectHash, err)
		}
		createFile(filePath, object.Data)
	}
}

func name(oh model.ObjectHeader) string {
	for _, meta := range oh.Meta {
		if meta.Key == "name" {
			return meta.Value
		}
	}
	return ""
}

func isDirectory(oh model.ObjectHeader) bool {
	for _, meta := range oh.Meta {
		if meta.Key == "type" && meta.Value == "directory" {
			return true
		}
	}
	return false
}

func (s *Service) retrieveHeaders(client *http.Client, rootHash model.RootHash, headerHashes ...string) error {
	headers, err := s.fetchObjectHeaders(client, headerHashes...)
	if err != nil {
		return fmt.Errorf("fetching object headers with hashes: %v from service failed due to error: %v", headerHashes, err)
	}
	var missingHeaderHashes []string
	for i, header := range headers {
		for _, ref := range header.ExternalReferences {
			_, err := s.db.GetObjectHeader(ref)
			if err != nil {
				if err == errors.ErrCannotFindObjectHeader {
					missingHeaderHashes = append(missingHeaderHashes, ref)
					continue
				}
				return fmt.Errorf("fetching object header with hash: %v from db failed due to error: %v", ref, err)
			} else {
				// update existing object header to newest sequence
				if err := s.db.UpdateObjectHeaderRootHashKey(ref, rootHash.Key()); err != nil {
					return fmt.Errorf("updating object header with hash: %v failed due to error: %v", ref, err)
				}
			}
		}
		// save missing object header
		if err := s.db.SaveObjectHeader(headerHashes[i], rootHash, header); err != nil {
			return fmt.Errorf("saving object header with hash: %v failed due to error: %v", headerHashes[i], err)
		}
	}

	if len(missingHeaderHashes) > 0 {
		if err := s.retrieveHeaders(client, rootHash, missingHeaderHashes...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) fetchObjectHeaders(client *http.Client, objectHeaderHashes ...string) ([]model.ObjectHeader, error) {
	objectHeadersResp := model.GetObjectHeadersResponse{}

	baseUrl := fmt.Sprint(s.config.TrackerAddress, "/data/object/header?hash=", objectHeaderHashes[0])
	additionalParams := ""
	for _, hash := range objectHeaderHashes[1:] {
		additionalParams = fmt.Sprint(additionalParams, "&hash=", hash)
	}

	url := fmt.Sprint(baseUrl, additionalParams)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return []model.ObjectHeader{}, fmt.Errorf("error creating request for fetching object headers with hashes: %v", objectHeaderHashes)
	}

	resp, err := client.Do(req)
	if err != nil {
		return []model.ObjectHeader{}, fmt.Errorf("request for object headers with hashes: %v failed due to error: %v", objectHeaderHashes, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			panic(err)
		}
	}()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []model.ObjectHeader{}, fmt.Errorf("error reading data: %v", err)
	}

	if objectHeaderErr := json.Unmarshal(data, &objectHeadersResp); objectHeaderErr != nil {
		return []model.ObjectHeader{}, fmt.Errorf("error unmarshaling received object headers response for with hashes: %v", objectHeaderHashes)
	}

	return objectHeadersResp.ObjectHeaders, nil
}

func (s *Service) fetchObject(client *http.Client, objectHash string) (model.Object, error) {
	object := model.Object{}
	url := fmt.Sprint(s.config.TrackerAddress, "/data/object?hash=", objectHash)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return object, fmt.Errorf("error creating request for object with hash: %v", objectHash)
	}

	resp, err := client.Do(req)
	if err != nil {
		return object, fmt.Errorf("request for object failed due to error: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			panic(err)
		}
	}()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return object, fmt.Errorf("error reading data: %v", err)
	}

	if objectErr := json.Unmarshal(data, &object); objectErr != nil {
		return object, fmt.Errorf("error unmarshaling received object with hash: %v", objectHash)
	}

	return object, nil
}

func createFile(path string, content []byte) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			panic(err)
		}
	}()
	if _, err := f.Write(content); err != nil {
		panic(err)
	}
	if err = f.Sync(); err != nil {
		panic(err)
	}
}

func (s *Service) removeUnreferencedFiles(rootHashKey string) {
	for _, path := range s.db.RemoveUnreferencedObjects(rootHashKey) {
		if err := os.RemoveAll(path); err != nil {
			fmt.Printf("Deleting file: %v failed due to error: %v", path, err)
		}
	}
}
