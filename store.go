package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
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

	// RecordDaily records a cumulative usage reading for (user, date, provider) in
	// the daily_usage collection. The first reading of the day sets the opening
	// value; "today's spend" is then last - opening.
	RecordDaily(ctx context.Context, user, date, provider string, cumulative float64) (DailyEntry, error)
	// GetDaily returns all per-provider daily entries for a user on a given date.
	GetDaily(ctx context.Context, user, date string) ([]DailyEntry, error)
}

// DailyEntry is one provider's usage for one day. Today's spend = Last - Opening.
type DailyEntry struct {
	Provider string  `json:"provider" bson:"provider"`
	Opening  float64 `json:"opening"  bson:"opening"`
	Last     float64 `json:"last"     bson:"last"`
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
	coll  *mongo.Collection
	daily *mongo.Collection
}

func (m *mongoStore) Kind() string { return "mongodb" }

func (m *mongoStore) RecordDaily(ctx context.Context, user, date, provider string, cumulative float64) (DailyEntry, error) {
	id := user + "|" + date + "|" + provider
	_, err := m.daily.UpdateByID(ctx, id, bson.M{
		"$setOnInsert": bson.M{"user": user, "date": date, "provider": provider, "opening": cumulative},
		"$set":         bson.M{"last": cumulative, "updated": time.Now()},
	}, options.Update().SetUpsert(true))
	if err != nil {
		return DailyEntry{}, err
	}
	var doc DailyEntry
	err = m.daily.FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	return doc, err
}

func (m *mongoStore) GetDaily(ctx context.Context, user, date string) ([]DailyEntry, error) {
	cur, err := m.daily.Find(ctx, bson.M{"user": user, "date": date})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []DailyEntry
	for cur.Next(ctx) {
		var d DailyEntry
		if err := cur.Decode(&d); err == nil {
			out = append(out, d)
		}
	}
	return out, cur.Err()
}

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
	db := client.Database(dbName)
	return &mongoStore{
		coll:  db.Collection("settings"),
		daily: db.Collection("daily_usage"),
	}, nil
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
	mu    sync.Mutex
	m     map[string]map[string]any
	daily map[string]DailyEntry
}

func newMemStore() *memStore {
	return &memStore{
		m:     make(map[string]map[string]any),
		daily: make(map[string]DailyEntry),
	}
}

func (s *memStore) Kind() string { return "memory" }

func (s *memStore) RecordDaily(_ context.Context, user, date, provider string, cumulative float64) (DailyEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := user + "|" + date + "|" + provider
	e, ok := s.daily[id]
	if !ok {
		e = DailyEntry{Provider: provider, Opening: cumulative, Last: cumulative}
	} else {
		e.Last = cumulative
	}
	s.daily[id] = e
	return e, nil
}

func (s *memStore) GetDaily(_ context.Context, user, date string) ([]DailyEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := user + "|" + date + "|"
	var out []DailyEntry
	for id, e := range s.daily {
		if strings.HasPrefix(id, prefix) {
			out = append(out, e)
		}
	}
	return out, nil
}

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
