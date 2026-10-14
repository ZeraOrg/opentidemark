// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/opentidemark/internal/store"
)

const infoTimeout = 5 * time.Second

// Info reports server version and disk space (requires SELECT on system.disks).
type Info struct{ Conn driver.Conn }

// Info implements service.StorageInfo.
func (i Info) Info(ctx context.Context) (store.ServerInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()
	v, err := ServerVersion(ctx, i.Conn)
	if err != nil {
		return store.ServerInfo{}, err
	}
	out := store.ServerInfo{Version: v, Disks: []store.DiskInfo{}}
	rows, err := i.Conn.Query(ctx, "SELECT name, free_space, total_space FROM system.disks ORDER BY name")
	if err != nil {
		return out, fmt.Errorf("query disks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d store.DiskInfo
		if err := rows.Scan(&d.Name, &d.FreeBytes, &d.TotalBytes); err != nil {
			return out, fmt.Errorf("scan disks: %w", err)
		}
		out.Disks = append(out.Disks, d)
	}
	return out, rows.Err()
}
