package sync2

import (
	"time"

	"github.com/ReneKroon/ttlcache/v2"
)

type TransactionIDCache struct {
	cache *ttlcache.Cache
}

func NewTransactionIDCache() *TransactionIDCache {
	c := ttlcache.NewCache()
	c.SetTTL(5 * time.Minute)     // keep transaction IDs for 5 minutes before forgetting about them
	c.SkipTTLExtensionOnHit(true) // we don't care how many times they ask for the item, 5min is the limit.
	return &TransactionIDCache{
		cache: c,
	}
}

// Store a new transaction ID received via v2 /sync
func (c *TransactionIDCache) Store(userID, eventID, txnID string) {
	c.cache.Set(cacheKey(userID, eventID), txnID)
}

// Get a transaction ID previously stored.
func (c *TransactionIDCache) Get(userID, eventID string) string {
	val, _ := c.cache.Get(cacheKey(userID, eventID))
	if val != nil {
		return val.(string)
	}
	return ""
}

func cacheKey(userID, eventID string) string {
	return userID + " " + eventID
}
