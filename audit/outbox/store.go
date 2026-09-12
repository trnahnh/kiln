// Package outbox keeps a controller's pending audit events in its CRs' status (ADR-0022).
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/trnahnh/kiln/audit"
)

// Path is the JSON pointer of the pending list in every CR's status.
const Path = "/status/audit/pending"

// Store reads and clears the pending list of objects of type T. List returns the
// list inside an object; the caller keeps the object's status schema.
type Store[T any, PT interface {
	*T
	client.Object
}] struct {
	Client client.Client
	List   func(PT) *[]audit.Pending
}

// Key names obj the way Signal and the store expect.
func Key(obj client.Object) string {
	return client.ObjectKeyFromObject(obj).String()
}

func (s Store[T, PT]) get(ctx context.Context, key string) (PT, error) {
	ns, name, _ := strings.Cut(key, "/")
	obj := PT(new(T))
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (s Store[T, PT]) Pending(ctx context.Context, key string) ([]audit.Pending, error) {
	obj, err := s.get(ctx, key)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return append([]audit.Pending(nil), *s.List(obj)...), nil
}

// Delivered removes p by a JSON patch that first tests the element is still p at its
// index, so a reconcile that appended in the meantime is never overwritten; a failed test
// is returned and the drainer reloads.
func (s Store[T, PT]) Delivered(ctx context.Context, key string, p audit.Pending) error {
	obj, err := s.get(ctx, key)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	index := -1
	for i, q := range *s.List(obj) {
		if q.EventID == p.EventID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil
	}
	element := fmt.Sprintf("%s/%d", Path, index)
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": element + "/eventId", "value": p.EventID},
		{"op": "remove", "path": element},
	})
	if err != nil {
		return err
	}
	if err := s.Client.Status().Patch(ctx, obj, client.RawPatch(types.JSONPatchType, patch)); err != nil {
		return fmt.Errorf("clear %s from %s: %w", p.EventID, key, err)
	}
	return nil
}
