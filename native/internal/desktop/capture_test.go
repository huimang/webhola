package desktop

import (
	"context"
	"testing"

	capturepkg "webhola/native/internal/capture"
)

func TestHistoryClearAndBounds(t *testing.T) {
	a := newApp(context.Background())
	for i := 1; i <= capturepkg.MaxEntries+10; i++ {
		a.accept(capturepkg.Exchange{ID: uint64(i), URL: "/test"})
	}
	if len(a.entries) != capturepkg.MaxEntries || a.discarded != 10 {
		t.Fatal("history is not bounded")
	}
	a.accept(capturepkg.Exchange{ID: 1, Status: 200})
	if len(a.entries) != capturepkg.MaxEntries || a.entries[0].ID != 11 {
		t.Fatal("evicted response resurrected")
	}
	a.clear()
	a.accept(capturepkg.Exchange{ID: 900, Status: 200})
	if len(a.entries) != 0 {
		t.Fatal("cleared request resurrected")
	}
	a.accept(capturepkg.Exchange{ID: 1011, Method: "POST", URL: "/new", Status: 201})
	a.search = "201"
	if a.visibleCount() != 1 {
		t.Fatal("status search failed")
	}
}
