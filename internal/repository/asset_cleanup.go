package repository

import "context"

const assetCleanupAdvisoryLockID int64 = 0x4341524d41415354 // "CARMAAST"

type AssetCleanupStore interface {
	ListReferencedAssetKeys(context.Context) ([]string, error)
	TryAssetCleanupLock(context.Context) (unlock func(context.Context) error, acquired bool, err error)
}

func (p *Postgres) ListReferencedAssetKeys(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT photo_key AS storage_key FROM vehicles WHERE photo_key <> ''
		UNION
		SELECT storage_key FROM attachments WHERE storage_key <> ''
		ORDER BY storage_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (p *Postgres) TryAssetCleanupLock(ctx context.Context) (func(context.Context) error, bool, error) {
	return p.tryAdvisoryLock(ctx, assetCleanupAdvisoryLockID, "asset cleanup")
}
