package agent

import (
	"encoding/base64"
	"testing"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/tools"
)

func TestLoggableImagesBoundsWhatTheLogCarries(t *testing.T) {
	small := tools.Image{MediaType: model.MediaTypePNG, Data: []byte("x"), Width: 1, Height: 1}
	images := []tools.Image{
		{MediaType: "image/webp", Data: []byte("x"), Width: 1, Height: 1},
		{MediaType: model.MediaTypePNG, Data: []byte("x")},
		{MediaType: model.MediaTypePNG, Data: make([]byte, tools.MaxImageBytes+1), Width: 1, Height: 1},
		{},
		small,
	}

	got := loggableImages(images)
	if len(got) != 1 {
		t.Fatalf("recorded %d images, want 1", len(got))
	}
	if got[0].Data != base64.StdEncoding.EncodeToString(small.Data) || got[0].Bytes != len(small.Data) {
		t.Errorf("recorded image = %+v, want the small PNG", got[0])
	}
}
