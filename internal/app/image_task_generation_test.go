package app

import (
	"context"
	"errors"
	"testing"
)

func TestCollectImageTaskGenerationsContinuesAfterSlotFailure(t *testing.T) {
	firstFailure := errors.New("first slot failed")
	attempts := 0
	items, err := collectImageTaskGenerations(context.Background(), 3, func(context.Context) ([]upstreamImageResult, error) {
		attempts++
		if attempts == 1 {
			return nil, firstFailure
		}
		return []upstreamImageResult{{Bytes: []byte{byte(attempts)}}}, nil
	})
	if attempts != 3 || len(items) != 2 || items[0].Bytes[0] != 2 || items[1].Bytes[0] != 3 || !errors.Is(err, firstFailure) {
		t.Fatalf("collected images: attempts=%d items=%v err=%v", attempts, items, err)
	}
}

func TestCollectImageTaskGenerationsHandlesEmptyAndCancellation(t *testing.T) {
	attempts := 0
	items, err := collectImageTaskGenerations(context.Background(), 2, func(ctx context.Context) ([]upstreamImageResult, error) {
		attempts++
		if attempts == 1 {
			return nil, nil
		}
		return []upstreamImageResult{{Bytes: []byte("image")}}, nil
	})
	if attempts != 2 || len(items) != 1 || !errors.Is(err, errNoImageGenerated) {
		t.Fatalf("empty slot: attempts=%d items=%v err=%v", attempts, items, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	attempts = 0
	items, err = collectImageTaskGenerations(ctx, 3, func(context.Context) ([]upstreamImageResult, error) {
		attempts++
		cancel()
		return nil, errors.New("interrupted")
	})
	if attempts != 1 || len(items) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled generation: attempts=%d items=%v err=%v", attempts, items, err)
	}
}
