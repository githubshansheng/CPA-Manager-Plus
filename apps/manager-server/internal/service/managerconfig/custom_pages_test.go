package managerconfig

import (
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestMergeSubmittedManagerConfigCustomPagesSupportsUpdateClearAndLegacyOmission(t *testing.T) {
	service := &Service{}
	base := service.DefaultManagerConfig()
	base.CustomPages = []store.ManagerCustomPageConfig{
		{ID: "existing", Title: "Existing", URL: "https://existing.example.test"},
	}

	omitted := service.MergeSubmittedManagerConfig(base, store.ManagerConfig{})
	if !reflect.DeepEqual(omitted.CustomPages, base.CustomPages) {
		t.Fatalf("legacy omission cleared custom pages: %#v", omitted.CustomPages)
	}

	updated := service.MergeSubmittedManagerConfig(base, store.ManagerConfig{
		CustomPages: []store.ManagerCustomPageConfig{
			{ID: " status ", Title: " Status ", URL: " https://status.example.test "},
		},
	})
	want := []store.ManagerCustomPageConfig{
		{ID: "status", Title: "Status", URL: "https://status.example.test"},
	}
	if !reflect.DeepEqual(updated.CustomPages, want) {
		t.Fatalf("updated custom pages = %#v, want %#v", updated.CustomPages, want)
	}

	cleared := service.MergeSubmittedManagerConfig(base, store.ManagerConfig{
		CustomPages: []store.ManagerCustomPageConfig{},
	})
	if cleared.CustomPages == nil || len(cleared.CustomPages) != 0 {
		t.Fatalf("explicit empty custom pages did not clear the list: %#v", cleared.CustomPages)
	}
}

func TestPublicConfigIncludesCustomPages(t *testing.T) {
	pages := []store.ManagerCustomPageConfig{
		{ID: "status", Title: "Status", URL: "https://status.example.test"},
	}
	public := PublicConfig(store.ManagerConfig{CustomPages: pages})
	if !reflect.DeepEqual(public.CustomPages, pages) {
		t.Fatalf("public custom pages = %#v, want %#v", public.CustomPages, pages)
	}
}
