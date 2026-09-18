package ublox

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageBrokerFansOutAndFiltersMessages(t *testing.T) {
	broker := newMessageBroker()
	all := broker.Subscribe(context.Background())
	clock := broker.Subscribe(context.Background(), NavClockType)
	filtered := broker.SubscribeFunc(context.Background(), func(message Message) bool {
		clockMessage, ok := message.Payload.(NavClock)
		return ok && clockMessage.Offset > 10
	})
	defer all.Cancel()
	defer clock.Cancel()
	defer filtered.Cancel()

	broker.Publish(Message{Type: NavClockType, Payload: NavClock{Offset: 23}})
	broker.Publish(Message{Type: NavStatusType, Payload: NavStatus{GPSFix: 3}})

	message := <-all.Messages
	assert.Equal(t, NavClockType, message.Type)
	message = <-all.Messages
	assert.Equal(t, NavStatusType, message.Type)

	message = <-clock.Messages
	assert.Equal(t, NavClockType, message.Type)
	message = <-filtered.Messages
	assert.Equal(t, NavClockType, message.Type)
	select {
	case unexpected := <-clock.Messages:
		t.Fatalf("received unexpected message: %#v", unexpected)
	case <-time.After(10 * time.Millisecond):
	}
}

func TestSubscriptionCancelClosesChannel(t *testing.T) {
	broker := newMessageBroker()
	subscription := broker.Subscribe(context.Background(), NavClockType)
	subscription.Cancel()

	_, ok := <-subscription.Messages
	require.False(t, ok)

	// Cancellation is idempotent.
	subscription.Cancel()
}
