package app

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPartialImageSaveChargesOnlyDeliveredResults(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAuthKeys([]UserKey{{
		ID: "owner", Role: "user", Enabled: true,
		ImageDailyQuota: 10, ImageMonthlyQuota: 10, ImageTotalQuota: 10,
	}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	identity := &Identity{ID: "owner", Role: "user"}
	if !s.consumeImage(identity, 3) {
		t.Fatal("could not reserve three image credits")
	}
	items := []upstreamImageResult{{Bytes: []byte("first")}, {Bytes: []byte("second")}, {Bytes: []byte("third")}}
	called := 0
	data, err := collectSavedImageResults(items, 3, func(result upstreamImageResult) (map[string]any, error) {
		called++
		if called == 2 {
			return nil, errors.New("disk full")
		}
		return map[string]any{"url": string(result.Bytes)}, nil
	})
	if err == nil || len(data) != 1 || data[0]["url"] != "first" || called != 2 {
		t.Fatalf("partial save: data=%v err=%v called=%d", data, err, called)
	}
	s.refundImage(identity, 3-len(data))
	key := store.LoadAuthKeys()[0]
	if key.ImageDailyUsed != 1 || key.ImageMonthlyUsed != 1 || key.ImageTotalUsed != 1 {
		t.Fatalf("partial save quota = %+v", key)
	}
}

func TestImageSaveFailureBeforeFirstResultHasNoData(t *testing.T) {
	data, err := collectSavedImageResults([]upstreamImageResult{{Bytes: []byte("first")}}, 1, func(upstreamImageResult) (map[string]any, error) {
		return nil, errors.New("disk full")
	})
	if err == nil || len(data) != 0 {
		t.Fatalf("first save failure: data=%v err=%v", data, err)
	}
}
