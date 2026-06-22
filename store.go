package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Store persists per-user dashboard settings (provider API keys + usage numbers).
type Store interface {
	Get(ctx context.Context, user string) (map[string]any, error)
	Set(ctx context.Context, user string, data map[string]any) error
	Kind() string // "mongodb" or "memory"
}

// newStore returns a MongoDB Atlas-backed store when MONGODB_URI is set,
// otherwise an in-memory fallback so the app still runs locally.
func newStore() Store {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		log.Println("MONGODB_URI not set — using in-memory store (data lost on restart)")
		return newMemStore()
	}
	ms, err := newMongoStore(uri)
	if err != nil {
		log.Printf("MongoDB Atlas connection failed (%v) — falling back to in-memory store", err)
		return newMemStore()
	}
	log.Println("Connected to MongoDB Atlas")
	return ms
}

// ---------- MongoDB Atlas ----------

type mongoStore struct {
	coll *mongo.Collection
}

func (m *mongoStore) Kind() string { return "mongodb" }

func newMongoStore(uri string) (*mongoStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, err
	}
	dbName := os.Getenv("MONGODB_DB")
	if dbName == "" {
		dbName = "llmdash"
	}
	return &mongoStore{coll: client.Database(dbName).Collection("settings")}, nil
}

// We store the settings blob as a JSON string to avoid BSON key restrictions.
func (m *mongoStore) Get(ctx context.Context, user string) (map[string]any, error) {
	var doc struct {
		Data string `bson:"data"`
	}
	err := m.coll.FindOne(ctx, bson.M{"_id": user}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if doc.Data == "" {
		return nil, nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(doc.Data), &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (m *mongoStore) Set(ctx context.Context, user string, data map[string]any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = m.coll.UpdateOne(ctx,
		bson.M{"_id": user},
		bson.M{"$set": bson.M{"data": string(b), "updated": time.Now()}},
		options.Update().SetUpsert(true),
	)
	return err
}

// ---------- in-memory fallback ----------

type memStore struct {
	mu sync.Mutex
	m  map[string]map[string]any
}

func newMemStore() *memStore {
	return &memStore{m: make(map[string]map[string]any)}
}

func (s *memStore) Kind() string { return "memory" }

func (s *memStore) Get(_ context.Context, user string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[user], nil
}

func (s *memStore) Set(_ context.Context, user string, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[user] = data
	return nil
}
