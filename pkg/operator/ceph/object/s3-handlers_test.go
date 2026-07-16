/*
Copyright 2021 The Rook Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package object

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestNewS3Agent(t *testing.T) {
	accessKey := "accessKey"
	secretKey := "secretKey"
	endpoint := "endpoint"

	t.Run("test without tls/debug", func(t *testing.T) {
		debug := false
		insecure := false
		s3Agent, err := NewS3Agent(accessKey, secretKey, endpoint, debug, nil, insecure, nil)
		assert.NoError(t, err)
		assert.NotNil(t, s3Agent.Client)
		assert.Equal(t, "http://endpoint", *s3Agent.Client.Options().BaseEndpoint)
		assert.Equal(t, aws.ClientLogMode(0), s3Agent.Client.Options().ClientLogMode)
	})
	t.Run("test with debug without tls", func(t *testing.T) {
		debug := true
		insecure := false
		s3Agent, err := NewS3Agent(accessKey, secretKey, endpoint, debug, nil, insecure, nil)
		assert.NoError(t, err)
		assert.NotNil(t, s3Agent.Client)
		assert.Equal(t, "http://endpoint", *s3Agent.Client.Options().BaseEndpoint)
		assert.Equal(t, aws.LogSigning, s3Agent.Client.Options().ClientLogMode)
	})
	t.Run("test without tls client cert but insecure tls", func(t *testing.T) {
		debug := true
		insecure := true
		s3Agent, err := NewS3Agent(accessKey, secretKey, endpoint, debug, nil, insecure, nil)
		assert.NoError(t, err)
		assert.NotNil(t, s3Agent.Client)
		assert.Equal(t, "https://endpoint", *s3Agent.Client.Options().BaseEndpoint)
		httpClient := s3Agent.Client.Options().HTTPClient.(*http.Client)
		assert.NotNil(t, httpClient.Transport.(*http.Transport).TLSClientConfig.RootCAs)
		assert.True(t, httpClient.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify)
	})
	t.Run("test with secure tls client cert", func(t *testing.T) {
		debug := true
		insecure := false
		tlsCert := []byte("tlsCert")
		s3Agent, err := NewS3Agent(accessKey, secretKey, endpoint, debug, tlsCert, insecure, nil)
		assert.NoError(t, err)
		assert.NotNil(t, s3Agent.Client)
		assert.Equal(t, "https://endpoint", *s3Agent.Client.Options().BaseEndpoint)
		httpClient := s3Agent.Client.Options().HTTPClient.(*http.Client)
		assert.NotNil(t, httpClient.Transport.(*http.Transport).TLSClientConfig.RootCAs)
		assert.False(t, httpClient.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify)
	})
	t.Run("test with insecure tls client cert", func(t *testing.T) {
		debug := true
		insecure := true
		tlsCert := []byte("tlsCert")
		s3Agent, err := NewS3Agent(accessKey, secretKey, endpoint, debug, tlsCert, insecure, nil)
		assert.NoError(t, err)
		assert.NotNil(t, s3Agent.Client)
		assert.Equal(t, "https://endpoint", *s3Agent.Client.Options().BaseEndpoint)
		httpClient := s3Agent.Client.Options().HTTPClient.(*http.Client)
		assert.NotNil(t, httpClient.Transport)
		assert.True(t, httpClient.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify)
	})
	t.Run("test with custom http.Client", func(t *testing.T) {
		debug := true
		insecure := false
		httpClient := &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        7,
				MaxIdleConnsPerHost: 13,
				MaxConnsPerHost:     17,
			},
		}
		s3Agent, err := NewS3Agent(accessKey, secretKey, endpoint, debug, nil, insecure, httpClient)
		assert.NoError(t, err)
		assert.NotNil(t, s3Agent.Client)
		assert.Equal(t, "http://endpoint", *s3Agent.Client.Options().BaseEndpoint)
		resolvedClient := s3Agent.Client.Options().HTTPClient.(*http.Client)
		transport := resolvedClient.Transport.(*http.Transport)
		assert.Equal(t, 7, transport.MaxIdleConns)
		assert.Equal(t, 13, transport.MaxIdleConnsPerHost)
		assert.Equal(t, 17, transport.MaxConnsPerHost)
	})
	t.Run("endpoint with host:port for TLS", func(t *testing.T) {
		ep := "rook-ceph-rgw-store.test-ns.svc:443"
		s3Agent, err := NewS3Agent(accessKey, secretKey, ep, false, nil, true, nil)
		assert.NoError(t, err)
		assert.Equal(t, "https://rook-ceph-rgw-store.test-ns.svc:443", *s3Agent.Client.Options().BaseEndpoint)
	})
	t.Run("endpoint with host:port without TLS", func(t *testing.T) {
		ep := "rook-ceph-rgw-store.test-ns.svc:80"
		s3Agent, err := NewS3Agent(accessKey, secretKey, ep, false, nil, false, nil)
		assert.NoError(t, err)
		assert.Equal(t, "http://rook-ceph-rgw-store.test-ns.svc:80", *s3Agent.Client.Options().BaseEndpoint)
	})
	t.Run("endpoint with full URL", func(t *testing.T) {
		ep := "https://rook-ceph-rgw-store.test-ns.svc:443"
		s3Agent, err := NewS3Agent(accessKey, secretKey, ep, false, nil, true, nil)
		assert.NoError(t, err)
		assert.Equal(t, "https://rook-ceph-rgw-store.test-ns.svc:443", *s3Agent.Client.Options().BaseEndpoint)
	})
}

type capturingRoundTripper struct {
	req  *http.Request
	body []byte
}

func (rt *capturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.req = req
	if req.Body != nil {
		rt.body, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

func TestS3AgentCreateBucket(t *testing.T) {
	newAgent := func(t *testing.T, rt http.RoundTripper) *S3Agent {
		s3Agent, err := NewS3Agent("accessKey", "secretKey", "endpoint", false, nil, false, &http.Client{Transport: rt})
		assert.NoError(t, err)
		return s3Agent
	}

	t.Run("CreateBucket sends no CreateBucketConfiguration", func(t *testing.T) {
		rt := &capturingRoundTripper{}
		err := newAgent(t, rt).CreateBucket(context.TODO(), "bkt")
		assert.NoError(t, err)
		assert.Equal(t, http.MethodPut, rt.req.Method)
		assert.Contains(t, rt.req.URL.Path, "/bkt")
		assert.NotContains(t, string(rt.body), "CreateBucketConfiguration")
	})

	t.Run("CreateBucketWithPlacement sends the location verbatim", func(t *testing.T) {
		rt := &capturingRoundTripper{}
		err := newAgent(t, rt).CreateBucketWithPlacement(context.TODO(), "bkt", "my-store:loc-a", "")
		assert.NoError(t, err)
		assert.Equal(t, http.MethodPut, rt.req.Method)
		assert.Contains(t, rt.req.URL.Path, "/bkt")
		assert.Contains(t, string(rt.body), "<LocationConstraint>my-store:loc-a</LocationConstraint>")
		assert.Empty(t, rt.req.Header.Get("X-Amz-Storage-Class"))
	})

	t.Run("CreateBucketWithPlacement sends the storage class header", func(t *testing.T) {
		rt := &capturingRoundTripper{}
		err := newAgent(t, rt).CreateBucketWithPlacement(context.TODO(), "bkt", "my-store:loc-a", "FOO")
		assert.NoError(t, err)
		assert.Contains(t, string(rt.body), "<LocationConstraint>my-store:loc-a</LocationConstraint>")
		assert.Equal(t, "FOO", rt.req.Header.Get("X-Amz-Storage-Class"))
		// the header must be part of the SigV4 signature or RGW rejects it
		assert.Contains(t, rt.req.Header.Get("Authorization"), "x-amz-storage-class")
	})

	t.Run("storage class header without a location constraint", func(t *testing.T) {
		rt := &capturingRoundTripper{}
		err := newAgent(t, rt).CreateBucketWithPlacement(context.TODO(), "bkt", "", "FOO")
		assert.NoError(t, err)
		assert.NotContains(t, string(rt.body), "CreateBucketConfiguration")
		assert.Equal(t, "FOO", rt.req.Header.Get("X-Amz-Storage-Class"))
	})

	t.Run("empty location and storage class behave like CreateBucket", func(t *testing.T) {
		rt := &capturingRoundTripper{}
		err := newAgent(t, rt).CreateBucketWithPlacement(context.TODO(), "bkt", "", "")
		assert.NoError(t, err)
		assert.NotContains(t, string(rt.body), "CreateBucketConfiguration")
		assert.Empty(t, rt.req.Header.Get("X-Amz-Storage-Class"))
	})
}
