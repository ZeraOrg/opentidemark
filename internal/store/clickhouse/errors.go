// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"errors"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

// ErrRowRejected marks rows the client could not encode for the insert (deterministic, so
// retrying cannot help).
var ErrRowRejected = errors.New("row rejected")

// transientCodes are ClickHouse exception codes that may succeed on retry: overload, timeouts,
// replication/Keeper trouble, missing schema during migrations and permission fixes.
var transientCodes = map[int32]bool{
	16:  true, // NO_SUCH_COLUMN_IN_TABLE (migration pending on this replica)
	47:  true, // UNKNOWN_IDENTIFIER
	60:  true, // UNKNOWN_TABLE
	81:  true, // UNKNOWN_DATABASE
	159: true, // TIMEOUT_EXCEEDED
	164: true, // READONLY
	202: true, // TOO_MANY_SIMULTANEOUS_QUERIES
	203: true, // NO_FREE_CONNECTION
	209: true, // SOCKET_TIMEOUT
	210: true, // NETWORK_ERROR
	236: true, // ABORTED
	241: true, // MEMORY_LIMIT_EXCEEDED
	242: true, // TABLE_IS_READ_ONLY
	252: true, // TOO_MANY_PARTS
	285: true, // TOO_FEW_LIVE_REPLICAS
	286: true, // UNSATISFIED_QUORUM_FOR_PREVIOUS_WRITE
	319: true, // UNKNOWN_STATUS_OF_INSERT
	394: true, // QUERY_WAS_CANCELLED
	425: true, // SYSTEM_ERROR
	497: true, // ACCESS_DENIED (fixable by the operator; keep the data)
	516: true, // AUTHENTICATION_FAILED
	999: true, // KEEPER_EXCEPTION
}

// IsPermanent reports whether a failed insert can never succeed as is, so its rows belong in
// the dead-letter files (SPEC §9.8). Network errors, timeouts and the transient server codes
// above are retried.
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRowRejected) {
		return true
	}
	var ex *ch.Exception
	if errors.As(err, &ex) {
		return !transientCodes[ex.Code]
	}
	return false
}
