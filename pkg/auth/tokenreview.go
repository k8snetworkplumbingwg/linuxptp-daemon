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

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// defaultTokenCacheTTL bounds how long a positive TokenReview result is trusted
// without re-validating against the API server. It is intentionally short so
// that token revocation takes effect quickly while still absorbing bursts of
// requests carrying the same token.
const defaultTokenCacheTTL = 30 * time.Second

// maxTokenCacheEntries bounds the positive-result cache so a flood of distinct
// (attacker-supplied but briefly-valid, or simply many-tenant) tokens cannot
// grow the map without limit and exhaust memory (CWE-400). When the cache is
// full and no expired entry can be reclaimed, new results are simply not cached
// - correctness is unaffected because every cache miss re-validates against the
// API server.
const maxTokenCacheEntries = 4096

// tokenReviewer is the subset of the Kubernetes API used to validate tokens.
// It is satisfied by kubernetes.Interface and can be faked in tests.
type tokenReviewer interface {
	Create(ctx context.Context, tr *authnv1.TokenReview, opts metav1.CreateOptions) (*authnv1.TokenReview, error)
}

type cacheEntry struct {
	info      *TokenInfo
	expiresAt time.Time
}

// TokenReviewValidator validates bearer tokens using the Kubernetes TokenReview
// API. It handles both projected ServiceAccount JWTs and opaque OpenShift OAuth
// access tokens (which cannot be verified via JWKS), and honours revocation
// because every cache miss consults the API server. It implements
// TokenValidator.
type TokenReviewValidator struct {
	reviewer tokenReviewer
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewTokenReviewValidator builds a validator using an in-cluster config (or
// KUBECONFIG when set). The pod's ServiceAccount must be bound to the
// system:auth-delegator ClusterRole so it may create TokenReviews.
func NewTokenReviewValidator() (*TokenReviewValidator, error) {
	var config *rest.Config
	var err error
	if kubeConfig := os.Getenv("KUBECONFIG"); kubeConfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to build kubernetes config for TokenReview: %w", err)
	}

	config.Timeout = 10 * time.Second
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client for TokenReview: %w", err)
	}

	return NewTokenReviewValidatorWithReviewer(clientset.AuthenticationV1().TokenReviews(), defaultTokenCacheTTL), nil
}

// NewTokenReviewValidatorWithReviewer builds a validator around a supplied
// reviewer (used for testing) and cache TTL. A non-positive ttl disables
// caching.
func NewTokenReviewValidatorWithReviewer(reviewer tokenReviewer, ttl time.Duration) *TokenReviewValidator {
	return &TokenReviewValidator{
		reviewer: reviewer,
		ttl:      ttl,
		cache:    make(map[string]cacheEntry),
	}
}

// cacheKey derives a stable, non-reversible key from the token and the required
// audiences so that a token accepted for one audience set is not reused for a
// different one. The raw token is never stored.
func cacheKey(token string, audiences []string) string {
	h := sha256.New()
	h.Write([]byte(token))
	for _, a := range audiences {
		h.Write([]byte{0})
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// storeLocked inserts an entry into the bounded positive cache. The caller must
// hold v.mu. When the cache is at capacity it first reclaims expired entries;
// if it is still full, the new entry is dropped rather than evicting a live one
// (a subsequent request simply re-validates against the API server).
func (v *TokenReviewValidator) storeLocked(key string, entry cacheEntry) {
	if _, exists := v.cache[key]; !exists && len(v.cache) >= maxTokenCacheEntries {
		now := time.Now()
		for k, e := range v.cache {
			if now.After(e.expiresAt) {
				delete(v.cache, k)
			}
		}
		if len(v.cache) >= maxTokenCacheEntries {
			return
		}
	}
	v.cache[key] = entry
}

// ValidateToken implements TokenValidator.
func (v *TokenReviewValidator) ValidateToken(ctx context.Context, token string, audiences []string) (*TokenInfo, error) {
	if token == "" {
		return nil, fmt.Errorf("empty bearer token")
	}

	key := cacheKey(token, audiences)
	if v.ttl > 0 {
		v.mu.Lock()
		if e, ok := v.cache[key]; ok && time.Now().Before(e.expiresAt) {
			v.mu.Unlock()
			return e.info, nil
		}
		v.mu.Unlock()
	}

	tr := &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{
			Token:     token,
			Audiences: audiences,
		},
	}

	result, err := v.reviewer.Create(ctx, tr, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("TokenReview request failed: %w", err)
	}

	if result.Status.Error != "" {
		return nil, fmt.Errorf("TokenReview rejected token: %s", result.Status.Error)
	}
	if !result.Status.Authenticated {
		return nil, fmt.Errorf("token is not authenticated")
	}

	// When audiences were requested, the API server echoes back the subset the
	// token is actually valid for. An empty intersection means the token was
	// not minted for us and must be rejected.
	if len(audiences) > 0 {
		matched := false
		for _, required := range audiences {
			for _, actual := range result.Status.Audiences {
				if actual == required {
					matched = true
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf("token audience does not match required audiences %v", audiences)
		}
	}

	info := &TokenInfo{
		Username:  result.Status.User.Username,
		UID:       result.Status.User.UID,
		Groups:    result.Status.User.Groups,
		Audiences: result.Status.Audiences,
	}

	if v.ttl > 0 {
		v.mu.Lock()
		v.storeLocked(key, cacheEntry{info: info, expiresAt: time.Now().Add(v.ttl)})
		v.mu.Unlock()
	}

	log.Debugf("TokenReview authenticated user %q (uid=%s)", info.Username, info.UID)
	return info, nil
}
