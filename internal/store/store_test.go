package store

import (
	"context"
	"errors"
	"testing"
)

func TestDirIdempotentOverwrite(t *testing.T) {
	d := Dir{Root: t.TempDir()}
	ctx := context.Background()
	for _, body := range []string{"one", "two"} {
		if err := d.Put(ctx, RawKey("2026-10-18"), []byte(body), "application/json"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.Get(ctx, RawKey("2026-10-18"))
	if err != nil || string(got) != "two" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := d.Get(ctx, RawKey("1999-01-01")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestKeys(t *testing.T) {
	if CuratedKey("roles", "2026-10-18") != "curated/roles/dt=2026-10-18/roles.parquet" {
		t.Fatal("curated key")
	}
}
