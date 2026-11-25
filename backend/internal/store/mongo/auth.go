package mongostore

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
)

func (s *Store) CreateUser(ctx context.Context, user *auth.User) error {
	_, err := s.db.Collection(ColUsers).InsertOne(ctx, user)
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrAlreadyExists
	}
	return err
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*auth.User, error) {
	var u auth.User
	err := s.db.Collection(ColUsers).FindOne(ctx, bson.M{"_id": id}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, auth.ErrNotFound
	}
	return &u, err
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*auth.User, error) {
	var u auth.User
	err := s.db.Collection(ColUsers).FindOne(ctx, bson.M{"username": username}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, auth.ErrNotFound
	}
	return &u, err
}

func (s *Store) ListUsers(ctx context.Context) ([]*auth.User, error) {
	cur, err := s.db.Collection(ColUsers).Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*auth.User
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpdateUser(ctx context.Context, user *auth.User) error {
	res, err := s.db.Collection(ColUsers).ReplaceOne(ctx, bson.M{"_id": user.ID}, user)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	res, err := s.db.Collection(ColUsers).DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	return s.db.Collection(ColUsers).CountDocuments(ctx, bson.M{})
}

func (s *Store) CreateSession(ctx context.Context, session *auth.Session) error {
	_, err := s.db.Collection(ColSessions).InsertOne(ctx, session)
	return err
}

func (s *Store) GetSessionByTokenHash(ctx context.Context, tokenHash string) (*auth.Session, error) {
	var sess auth.Session
	err := s.db.Collection(ColSessions).FindOne(ctx, bson.M{"token_hash": tokenHash}).Decode(&sess)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, auth.ErrNotFound
	}
	return &sess, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.Collection(ColSessions).DeleteOne(ctx, bson.M{"token_hash": tokenHash})
	return err
}

func (s *Store) DeleteSessionsByUser(ctx context.Context, userID string) error {
	_, err := s.db.Collection(ColSessions).DeleteMany(ctx, bson.M{"user_id": userID})
	return err
}

func (s *Store) CreateToken(ctx context.Context, token *auth.AccessToken) error {
	_, err := s.db.Collection(ColTokens).InsertOne(ctx, token)
	return err
}

func (s *Store) GetTokenByID(ctx context.Context, id string) (*auth.AccessToken, error) {
	var tok auth.AccessToken
	err := s.db.Collection(ColTokens).FindOne(ctx, bson.M{"_id": id}).Decode(&tok)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, auth.ErrNotFound
	}
	return &tok, err
}

func (s *Store) GetTokenByHash(ctx context.Context, tokenHash string) (*auth.AccessToken, error) {
	var tok auth.AccessToken
	err := s.db.Collection(ColTokens).FindOne(ctx, bson.M{"token_hash": tokenHash}).Decode(&tok)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, auth.ErrNotFound
	}
	return &tok, err
}

func (s *Store) ListTokens(ctx context.Context, registryID string) ([]*auth.AccessToken, error) {
	filter := bson.M{}
	if registryID != "" {
		filter["registry_id"] = registryID
	}
	cur, err := s.db.Collection(ColTokens).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*auth.AccessToken
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpdateTokenLastUsed(ctx context.Context, id string, ts time.Time) error {
	_, err := s.db.Collection(ColTokens).UpdateOne(
		ctx,
		bson.M{"_id": id},
		bson.M{"$set": bson.M{"last_used_at": ts}},
	)
	return err
}

func (s *Store) RevokeToken(ctx context.Context, id string) error {
	res, err := s.db.Collection(ColTokens).UpdateOne(
		ctx,
		bson.M{"_id": id},
		bson.M{"$set": bson.M{"revoked": true}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteToken(ctx context.Context, id string) error {
	res, err := s.db.Collection(ColTokens).DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}
