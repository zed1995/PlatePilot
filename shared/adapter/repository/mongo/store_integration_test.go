package mongo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/adapter/repository/contract"
	"github.com/zed/platepilot/shared/adapter/repository/mongo"
)

// TestAtlasReadiness exercises EnsureSchema/EnsureIndexes and the full write
// contract against a real Atlas cluster. It is skipped unless MONGO_URI is set,
// so the default `go test ./...` stays offline.
func TestAtlasReadiness(t *testing.T) {
	client := connectAtlas(t, t.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := client.DropCollections(ctx); err != nil {
		t.Fatalf("drop collections: %v", err)
	}
	first, err := client.EnsureSchema(ctx)
	if err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	for _, status := range first {
		if !status.Created {
			t.Errorf("first EnsureSchema: %s reported existing", status.Name)
		}
	}
	second, err := client.EnsureSchema(ctx)
	if err != nil {
		t.Fatalf("EnsureSchema (repeat): %v", err)
	}
	for _, status := range second {
		if status.Created {
			t.Errorf("second EnsureSchema: %s reported created", status.Name)
		}
	}
	if _, err := client.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	if _, err := client.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes (repeat): %v", err)
	}
}

// TestAtlasStoresSatisfyContract runs the shared write-port contract against
// Atlas. Skipped unless MONGO_URI is set.
func TestAtlasStoresSatisfyContract(t *testing.T) {
	client := connectAtlas(t, t.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := client.DropCollections(ctx); err != nil {
		t.Fatalf("drop collections: %v", err)
	}
	if _, err := client.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if _, err := client.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	contract.Run(t, func(t *testing.T) contract.Stores {
		if err := client.DropCollections(context.Background()); err != nil {
			t.Fatalf("reset collections: %v", err)
		}
		if _, err := client.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("re-ensure schema: %v", err)
		}
		return contract.Stores{
			Restaurants: mongo.NewRestaurantStore(client),
			Reviews:     mongo.NewReviewStore(client),
			Pipeline:    mongo.NewPipelineStore(client),
		}
	})
}

func connectAtlas(t *testing.T, testName string) *mongo.Client {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not set; skipping Atlas integration test")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "platepilot_test"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, mongo.Config{URI: uri, Database: database, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("%s: connect: %v", testName, err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client
}
