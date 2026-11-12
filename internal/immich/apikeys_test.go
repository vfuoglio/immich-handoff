package immich

import (
	"strings"
	"testing"
)

func TestTheFromTagNamesWhoACopyIsFromAndKeepsOneTagPerPerson(t *testing.T) {
	for in, want := range map[string]string{"Bob": "from/Bob", " Ann Lee ": "from/Ann Lee", "a/b": "from/a-b", "": "from/someone", "   ": "from/someone"} {
		if got := FromTag(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestAPoolKeyCanDoWhatASenderThatKeepsItsOriginalsAndAReceiverCanDoAndNothingMore(t *testing.T) {
	for _, album := range []bool{false, true} {
		for _, del := range []bool{false, true} {
			got := map[Permission]bool{}
			for _, p := range PoolPermissionsFor(album, del) {
				if got[p] {
					t.Errorf("%v listed twice", p)
				}
				got[p] = true
			}
			for _, p := range SenderPermissionsCopy {
				if !got[p] {
					t.Errorf("album=%v delete=%v: lacks the sender's %s", album, del, p)
				}
			}
			for _, p := range ReceiverPermissionsFor(album, del) {
				if !got[p] {
					t.Errorf("album=%v delete=%v: lacks the receiver's %s", album, del, p)
				}
			}
			if got["asset.delete"] != del {
				t.Errorf("album=%v delete=%v: asset.delete is %v; a pool key may delete only when the administrator allows moving copies to the trash", album, del, got["asset.delete"])
			}
			if got["album.create"] != album {
				t.Errorf("album=%v: album.create is %v", album, got["album.create"])
			}
			for p := range got {
				if strings.HasPrefix(string(p), "apiKey") || strings.HasPrefix(string(p), "user.update") || p == "all" {
					t.Errorf("a pool key must not be able to %s", p)
				}
			}
		}
	}
}
