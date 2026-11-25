package mongostore

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/amkargar/mirror-registry/backend/internal/registry"
)

func (s *Store) CreateInstance(ctx context.Context, inst *registry.Instance) error {
	_, err := s.db.Collection(ColRegistries).InsertOne(ctx, inst)
	if mongo.IsDuplicateKeyError(err) {
		return registry.ErrSlugExists
	}
	return err
}

func (s *Store) GetInstanceByID(ctx context.Context, id string) (*registry.Instance, error) {
	var inst registry.Instance
	err := s.db.Collection(ColRegistries).FindOne(ctx, bson.M{"_id": id}).Decode(&inst)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, registry.ErrInstanceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

func (s *Store) GetInstanceBySlug(ctx context.Context, slug string) (*registry.Instance, error) {
	var inst registry.Instance
	err := s.db.Collection(ColRegistries).FindOne(ctx, bson.M{"slug": slug}).Decode(&inst)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, registry.ErrInstanceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

func (s *Store) ListInstances(ctx context.Context) ([]*registry.Instance, error) {
	cur, err := s.db.Collection(ColRegistries).Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var out []*registry.Instance
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpdateInstance(ctx context.Context, inst *registry.Instance) error {
	res, err := s.db.Collection(ColRegistries).ReplaceOne(ctx, bson.M{"_id": inst.ID}, inst)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return registry.ErrInstanceNotFound
	}
	return nil
}

func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	res, err := s.db.Collection(ColRegistries).DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return registry.ErrInstanceNotFound
	}
	return nil
}
