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
	"encoding/json"
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/plugin/pkg/authenticator/token/webhook"
	authenticationclient "k8s.io/client-go/kubernetes/typed/authentication/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// defaultTokenCacheTTL bounds how long a positive TokenReview result is trusted
// without re-validating against the API server. It is intentionally short so
// that token revocation takes effect quickly while still absorbing bursts of
// requests carrying the same token.
const defaultTokenCacheTTL = 30 * time.Second

// Cache only successful reviews, bounded in both time and cardinality.
const maxTokenCacheEntries = 4096

// TokenReviewValidator authenticates cluster tokens with an audience-scoped positive cache.
type TokenReviewValidator struct {
	reviewer authenticator.Token
	ttl      time.Duration
	cache    *cache.LRUExpireCache
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
	return newTokenReviewValidator(config, defaultTokenCacheTTL)
}

func newTokenReviewValidator(config *rest.Config, ttl time.Duration) (*TokenReviewValidator, error) {
	client, err := authenticationclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	reviewer, err := webhook.NewFromInterface(client, nil, wait.Backoff{Steps: 1}, config.Timeout, webhook.AuthenticatorMetrics{
		RecordRequestTotal:   func(context.Context, string) {},
		RecordRequestLatency: func(context.Context, string, float64) {},
	})
	if err != nil {
		return nil, err
	}
	return &TokenReviewValidator{
		reviewer: reviewer,
		ttl:      ttl,
		cache:    cache.NewLRUExpireCache(maxTokenCacheEntries),
	}, nil
}

// ValidateToken authenticates the token for the requested audiences.
func (v *TokenReviewValidator) ValidateToken(ctx context.Context, token string, audiences []string) (*TokenInfo, error) {
	if token == "" {
		return nil, fmt.Errorf("empty bearer token")
	}
	// Hash an unambiguously encoded tuple so cached keys never retain bearer credentials.
	encoded, err := json.Marshal(struct {
		Token     []byte
		Audiences []string
	}{[]byte(token), audiences})
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(encoded)
	if cached, ok := v.cache.Get(key); ok {
		return cached.(*TokenInfo), nil
	}
	if len(audiences) > 0 {
		ctx = authenticator.WithAudiences(ctx, audiences)
	}
	result, ok, err := v.reviewer.AuthenticateToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("token or audience rejected")
	}
	info := &TokenInfo{
		Username:  result.User.GetName(),
		UID:       result.User.GetUID(),
		Groups:    result.User.GetGroups(),
		Audiences: result.Audiences,
	}
	if v.ttl > 0 {
		v.cache.Add(key, info, v.ttl)
	}
	log.Debugf("TokenReview authenticated user %q (uid=%s)", info.Username, info.UID)
	return info, nil
}
