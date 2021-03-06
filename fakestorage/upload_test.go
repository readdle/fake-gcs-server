// Copyright 2017 Francisco Souza. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fakestorage

import (
	"crypto/tls"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/context"
	"google.golang.org/api/googleapi"
)

func TestServerClientObjectWriter(t *testing.T) {
	const baseContent = "some nice content"
	content := strings.Repeat(baseContent+"\n", googleapi.MinUploadChunkSize)

	var tests = []struct {
		testCase  string
		chunkSize int
	}{
		{
			"default chunk size",
			googleapi.DefaultUploadChunkSize,
		},
		{
			"small chunk size",
			googleapi.MinUploadChunkSize,
		},
	}

	for _, test := range tests {
		t.Run(test.testCase, func(t *testing.T) {
			server := NewServer(nil)
			defer server.Stop()
			server.CreateBucket("some-bucket")
			client := server.Client()

			objHandle := client.Bucket("some-bucket").Object("some/interesting/object.txt")
			w := objHandle.NewWriter(context.Background())
			w.ChunkSize = test.chunkSize
			w.Write([]byte(content))
			err := w.Close()
			if err != nil {
				t.Fatal(err)
			}

			obj, err := server.GetObject("some-bucket", "some/interesting/object.txt")
			if err != nil {
				t.Fatal(err)
			}
			if string(obj.Content) != content {
				n := strings.Count(string(obj.Content), baseContent)
				t.Errorf("wrong content returned\nwant %dx%q\ngot  %dx%q",
					googleapi.MinUploadChunkSize, baseContent,
					n, baseContent)
			}
		})
	}
}

func TestServerClientObjectWriterBucketNotFound(t *testing.T) {
	server := NewServer(nil)
	defer server.Stop()
	client := server.Client()
	objHandle := client.Bucket("some-bucket").Object("some/interesting/object.txt")
	w := objHandle.NewWriter(context.Background())
	w.Write([]byte("whatever"))
	err := w.Close()
	if err == nil {
		t.Fatal("unexpected <nil> error")
	}
}

func TestServerClientSimpleUpload(t *testing.T) {
	server := NewServer(nil)
	defer server.Stop()
	server.CreateBucket("other-bucket")

	const data = "some nice content"
	req, err := http.NewRequest("POST", server.URL()+"/storage/v1/b/other-bucket/o?uploadType=media&name=some/nice/object.txt", strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	expectedStatus := http.StatusOK
	if resp.StatusCode != expectedStatus {
		t.Errorf("wrong status code\nwant %d\ngot  %d", expectedStatus, resp.StatusCode)
	}

	obj, err := server.GetObject("other-bucket", "some/nice/object.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(obj.Content) != data {
		t.Errorf("wrong content\nwant %q\ngot  %q", string(obj.Content), data)
	}
}

func TestServerClientSimpleUploadNoName(t *testing.T) {
	server := NewServer(nil)
	defer server.Stop()
	server.CreateBucket("other-bucket")

	const data = "some nice content"
	req, err := http.NewRequest("POST", server.URL()+"/storage/v1/b/other-bucket/o?uploadType=media", strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	expectedStatus := http.StatusBadRequest
	if resp.StatusCode != expectedStatus {
		t.Errorf("wrong status returned\nwant %d\ngot  %d", expectedStatus, resp.StatusCode)
	}
}

func TestServerInvalidUploadType(t *testing.T) {
	server := NewServer(nil)
	defer server.Stop()
	server.CreateBucket("other-bucket")
	const data = "some nice content"
	req, err := http.NewRequest("POST", server.URL()+"/storage/v1/b/other-bucket/o?uploadType=bananas&name=some-object.txt", strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	expectedStatus := http.StatusBadRequest
	if resp.StatusCode != expectedStatus {
		t.Errorf("wrong status returned\nwant %d\ngot  %d", expectedStatus, resp.StatusCode)
	}
}
