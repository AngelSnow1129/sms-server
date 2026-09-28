package d1

import (
	"database/sql"
	"fmt"
	"syscall/js"
)

type result struct {
	resultObj js.Value
}

var _ sql.Result = (*result)(nil)

// LastInsertId returns id of result's last row.
// If 'last_row_id' can't be retrieved, this method returns error.
func (r *result) LastInsertId() (int64, error) {
	return r.numberFromMeta("last_row_id")
}

// RowsAffected returns the number of rows affected by an update, insert, or delete.
// If 'changes' can't be retrieved, this method returns error.
func (r *result) RowsAffected() (int64, error) {
	return r.numberFromMeta("changes")
}

func (r *result) numberFromMeta(key string) (int64, error) {
	v := r.resultObj.Get("meta").Get(key)
	if v.IsNull() || v.IsUndefined() {
		return 0, fmt.Errorf("d1: '%s' cannot be retrieved", key)
	}
	return int64(v.Int()), nil
}
