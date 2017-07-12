// Copyright 2017 Francisco Souza. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fakestorage

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

type multipartMetadata struct {
	Name string `json:"name"`
}

func (s *Server) insertObject(w http.ResponseWriter, r *http.Request) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	bucketName := mux.Vars(r)["bucketName"]
	if _, ok := s.buckets[bucketName]; !ok {
		w.WriteHeader(http.StatusNotFound)
		err := newErrorResponse(http.StatusNotFound, "Not found", nil)
		json.NewEncoder(w).Encode(err)
		return
	}
	uploadType := r.URL.Query().Get("uploadType")
	switch uploadType {
	case "media":
		s.simpleUpload(bucketName, w, r)
	case "multipart":
		s.multipartUpload(bucketName, w, r)
	case "resumable":
		s.resumableUpload(bucketName, w, r)
	default:
		http.Error(w, "invalid uploadType", http.StatusBadRequest)
	}
}

func (s *Server) copyObject(w http.ResponseWriter, r *http.Request) {
	encoder := json.NewEncoder(w)

	bucket1 := mux.Vars(r)["bucketName1"]
	bucket2 := mux.Vars(r)["bucketName2"]

	obj1 := mux.Vars(r)["obj1"]
	obj2 := mux.Vars(r)["obj2"]

	data, err := s.GetObject(bucket1, obj1)
	if err != nil {
		errResp := newErrorResponse(http.StatusNotFound, "Not Found", nil)
		w.WriteHeader(http.StatusNotFound)
		encoder.Encode(errResp)
		return
	}

	s.CreateObject(Object{
		Name:       obj2,
		BucketName: bucket2,
		Content:    data.Content,
	})

	w.WriteHeader(200)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"done": true,
	})
}

func (s *Server) simpleUpload(bucketName string, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name is required for simple uploads", http.StatusBadRequest)
		return
	}
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	obj := Object{BucketName: bucketName, Name: name, Content: data}
	s.buckets[bucketName] = append(s.buckets[bucketName], obj)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(obj)
}

func (s *Server) multipartUpload(bucketName string, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "invalid Content-Type header", http.StatusBadRequest)
		return
	}
	var (
		metadata *multipartMetadata
		content  []byte
	)
	reader := multipart.NewReader(r.Body, params["boundary"])
	part, err := reader.NextPart()
	for ; err == nil; part, err = reader.NextPart() {
		if metadata == nil {
			metadata, err = loadMetadata(part)
		} else {
			content, err = loadContent(part)
		}
		if err != nil {
			break
		}
	}
	if err != io.EOF {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	obj := Object{BucketName: bucketName, Name: metadata.Name, Content: content}
	s.buckets[bucketName] = append(s.buckets[bucketName], obj)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(obj)
}

func (s *Server) resumableUpload(bucketName string, w http.ResponseWriter, r *http.Request) {
	objName := r.URL.Query().Get("name")
	if objName == "" {
		metadata, err := loadMetadata(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		objName = metadata.Name
	}
	obj := Object{BucketName: bucketName, Name: objName}
	uploadID, err := generateUploadID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.uploads[uploadID] = obj
	w.Header().Set("Location", s.URL()+"/upload/resumable/"+uploadID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(obj)
}

func (s *Server) uploadFileContent(w http.ResponseWriter, r *http.Request) {
	uploadID := mux.Vars(r)["uploadId"]
	s.mtx.Lock()
	defer s.mtx.Unlock()
	obj, ok := s.uploads[uploadID]
	if !ok {
		http.Error(w, "upload not found", http.StatusNotFound)
		return
	}
	content, err := loadContent(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	commit := true
	status := http.StatusCreated
	objLength := len(obj.Content)
	obj.Content = append(obj.Content, content...)
	if contentRange := r.Header.Get("Content-Range"); contentRange != "" {
		commit, err = parseRange(contentRange, objLength, len(content), w)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if commit {
		delete(s.uploads, uploadID)
		s.buckets[obj.BucketName] = append(s.buckets[obj.BucketName], obj)
	} else {
		status = http.StatusOK
		w.Header().Set("X-Http-Status-Code-Override", "308")
		s.uploads[uploadID] = obj
	}
	data, _ := json.Marshal(obj)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	w.Write(data)
}

func parseRange(r string, objLength, bodyLength int, w http.ResponseWriter) (finished bool, err error) {
	invalidErr := fmt.Errorf("invalid Content-Range: %v", r)
	const bytesPrefix = "bytes "
	var contentLength int
	if !strings.HasPrefix(r, bytesPrefix) {
		return false, invalidErr
	}
	parts := strings.SplitN(r[len(bytesPrefix):], "/", 2)
	if len(parts) != 2 {
		return false, invalidErr
	}
	var rangeStart, rangeEnd int

	if parts[0] == "*" {
		rangeStart = objLength
		rangeEnd = objLength + bodyLength
	} else {
		rangeParts := strings.SplitN(parts[0], "-", 2)
		if len(rangeParts) != 2 {
			return false, invalidErr
		}
		rangeStart, err = strconv.Atoi(rangeParts[0])
		if err != nil {
			return false, invalidErr
		}
		rangeEnd, err = strconv.Atoi(rangeParts[1])
		if err != nil {
			return false, invalidErr
		}
	}

	contentLength = objLength + bodyLength
	finished = rangeEnd == contentLength
	w.Header().Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd))

	return finished, nil
}

func loadMetadata(rc io.ReadCloser) (*multipartMetadata, error) {
	defer rc.Close()
	var m multipartMetadata
	err := json.NewDecoder(rc).Decode(&m)
	return &m, err
}

func loadContent(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	return ioutil.ReadAll(rc)
}

func generateUploadID() (string, error) {
	var raw [16]byte
	_, err := rand.Read(raw[:])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", raw[:]), nil
}
