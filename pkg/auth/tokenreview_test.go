// Copyright 2025 The Cloud Native Events Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build unittests
// +build unittests

package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeReviewer is a programmable tokenReviewer for tests.
type fakeReviewer struct {
	calls  int
	handle func(tr *authnv1.TokenReview) (*authnv1.TokenReview, error)
}

func (f *fakeReviewer) Create(_ context.Context, tr *authnv1.TokenReview, _ metav1.CreateOptions) (*authnv1.TokenReview, error) {
	f.calls++
	return f.handle(tr)
}

func authenticated(user string, audiences []string) func(tr *authnv1.TokenReview) (*authnv1.TokenReview, error) {
	return func(tr *authnv1.TokenReview) (*authnv1.TokenReview, error) {
		return &authnv1.TokenReview{
			Status: authnv1.TokenReviewStatus{
				Authenticated: true,
				User:          authnv1.UserInfo{Username: user, UID: "uid-" + user},
				Audiences:     audiences,
			},
		}, nil
	}
}

func TestValidateToken_Success(t *testing.T) {
	fr := &fakeReviewer{handle: authenticated("system:serviceaccount:ns:sa", []string{"aud1"})}
	v := NewTokenReviewValidatorWithReviewer(fr, 0)

	info, err := v.ValidateToken(context.Background(), "tok", []string{"aud1"})
	assert.NoError(t, err)
	assert.Equal(t, "system:serviceaccount:ns:sa", info.Username)
	assert.Equal(t, []string{"aud1"}, info.Audiences)
}

func TestValidateToken_NotAuthenticated(t *testing.T) {
	fr := &fakeReviewer{handle: func(_ *authnv1.TokenReview) (*authnv1.TokenReview, error) {
		return &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Authenticated: false}}, nil
	}}
	v := NewTokenReviewValidatorWithReviewer(fr, 0)

	_, err := v.ValidateToken(context.Background(), "tok", nil)
	assert.Error(t, err)
}

func TestValidateToken_StatusError(t *testing.T) {
	fr := &fakeReviewer{handle: func(_ *authnv1.TokenReview) (*authnv1.TokenReview, error) {
		return &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Error: "expired"}}, nil
	}}
	v := NewTokenReviewValidatorWithReviewer(fr, 0)

	_, err := v.ValidateToken(context.Background(), "tok", nil)
	assert.Error(t, err)
}

func TestValidateToken_APIError(t *testing.T) {
	fr := &fakeReviewer{handle: func(_ *authnv1.TokenReview) (*authnv1.TokenReview, error) {
		return nil, fmt.Errorf("api down")
	}}
	v := NewTokenReviewValidatorWithReviewer(fr, 0)

	_, err := v.ValidateToken(context.Background(), "tok", nil)
	assert.Error(t, err)
}

func TestValidateToken_AudienceMismatch(t *testing.T) {
	// Requested an audience but the server returned none => reject.
	fr := &fakeReviewer{handle: authenticated("u", nil)}
	v := NewTokenReviewValidatorWithReviewer(fr, 0)

	_, err := v.ValidateToken(context.Background(), "tok", []string{"required-aud"})
	assert.Error(t, err)
}

func TestValidateToken_EmptyToken(t *testing.T) {
	fr := &fakeReviewer{handle: authenticated("u", nil)}
	v := NewTokenReviewValidatorWithReviewer(fr, 0)

	_, err := v.ValidateToken(context.Background(), "", nil)
	assert.Error(t, err)
	assert.Equal(t, 0, fr.calls, "must not call API for empty token")
}

func TestValidateToken_UnrelatedAudience(t *testing.T) {
	fr := &fakeReviewer{handle: authenticated("u", []string{"other-api"})}
	v := NewTokenReviewValidatorWithReviewer(fr, 0)
	_, err := v.ValidateToken(context.Background(), "tok", []string{"event-api"})
	assert.Error(t, err)
}

func TestValidateToken_CacheHit(t *testing.T) {
	fr := &fakeReviewer{handle: authenticated("u", []string{"aud1"})}
	v := NewTokenReviewValidatorWithReviewer(fr, time.Minute)

	_, err := v.ValidateToken(context.Background(), "tok", []string{"aud1"})
	assert.NoError(t, err)
	_, err = v.ValidateToken(context.Background(), "tok", []string{"aud1"})
	assert.NoError(t, err)
	assert.Equal(t, 1, fr.calls, "second identical request should be served from cache")
}

func TestValidateToken_CacheKeyDistinctByAudience(t *testing.T) {
	fr := &fakeReviewer{handle: authenticated("u", []string{"aud1", "aud2"})}
	v := NewTokenReviewValidatorWithReviewer(fr, time.Minute)

	_, err := v.ValidateToken(context.Background(), "tok", []string{"aud1"})
	assert.NoError(t, err)
	_, err = v.ValidateToken(context.Background(), "tok", []string{"aud2"})
	assert.NoError(t, err)
	assert.Equal(t, 2, fr.calls, "different audiences must not share a cache entry")
}

func TestValidateToken_CacheExpiry(t *testing.T) {
	fr := &fakeReviewer{handle: authenticated("u", []string{"aud1"})}
	v := NewTokenReviewValidatorWithReviewer(fr, 10*time.Millisecond)

	_, err := v.ValidateToken(context.Background(), "tok", []string{"aud1"})
	assert.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	_, err = v.ValidateToken(context.Background(), "tok", []string{"aud1"})
	assert.NoError(t, err)
	assert.Equal(t, 2, fr.calls, "expired cache entry must trigger re-validation")
}
