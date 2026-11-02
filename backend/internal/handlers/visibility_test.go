package handlers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadableMediaCond(t *testing.T) {
	tests := []struct {
		alias string
		n     int
		want  string // whitespace collapsed
	}{
		{"m", 1, "(m.owner_id = $1 OR EXISTS ( SELECT 1 FROM album_media am" +
			" JOIN album_members mem ON mem.album_id = am.album_id" +
			" WHERE am.media_id = m.id AND mem.user_id = $1))"},
		{"m", 2, "(m.owner_id = $2 OR EXISTS ( SELECT 1 FROM album_media am" +
			" JOIN album_members mem ON mem.album_id = am.album_id" +
			" WHERE am.media_id = m.id AND mem.user_id = $2))"},
		{"media", 3, "(media.owner_id = $3 OR EXISTS ( SELECT 1 FROM album_media am" +
			" JOIN album_members mem ON mem.album_id = am.album_id" +
			" WHERE am.media_id = media.id AND mem.user_id = $3))"},
	}
	for _, tt := range tests {
		got := readableMediaCond(tt.alias, tt.n)
		assert.Equal(t, tt.want, strings.Join(strings.Fields(got), " "))
	}
}
