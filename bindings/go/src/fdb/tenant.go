/*
 * tenant.go
 *
 * This source file is part of the FoundationDB open source project
 *
 * Copyright 2013-2024 Apple Inc. and the FoundationDB project authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// FoundationDB Go API

package fdb

// #define FDB_API_VERSION 730
// #include <foundationdb/fdb_c.h>
// #include <stdlib.h>
import "C"

import (
	"runtime"
	"sync"
)

const tenantMapKey = "\xff\xff/management/tenant/map/"

// CreateTenant creates a new tenant in the cluster. The tenant name cannot
// start with the \xff byte.
func (t Transaction) CreateTenant(name KeyConvertible) error {
	tenantName := name.FDBKey()
	if len(tenantName) == 0 || tenantName[0] == '\xff' {
		return errTenantNameInvalid
	}

	if err := t.Options().SetSpecialKeySpaceEnableWrites(); err != nil {
		return err
	}

	key := append(Key(tenantMapKey), tenantName...)

	exist, err := t.checkTenantExist(key)
	if err != nil {
		return err
	}
	if exist {
		return errTenantExists
	}

	t.Set(key, nil)

	return nil
}

// CreateTenant creates a new tenant in the cluster. The tenant name cannot
// start with the \xff byte.
func (d Database) CreateTenant(name KeyConvertible) error {
	_, err := d.Transact(func(t Transaction) (interface{}, error) {
		return nil, t.CreateTenant(name)
	})
	return err
}

// EnsureTenant creates a new tenant in the cluster only if
// it is not already present. The tenant name cannot
// start with the \xff byte. If the tenant already exists, no
// error is returned. The tenant is then opened and returned.
// Users should call Close() on the Tenant when they are
// done using it, to release the local resources.
func (d Database) EnsureTenant(name KeyConvertible) (Tenant, error) {
	_, err := d.Transact(func(t Transaction) (interface{}, error) {
		return nil, t.CreateTenant(name)
	})
	if err == errTenantExists {
		return Tenant{}, nil
	}
	if err != nil {
		return Tenant{}, err
	}
	return d.OpenTenant(name)
}

// DeleteTenant deletes an existing tenant in the cluster. If the provided tenant name doesn't exist an error will be thrown.
func (t Transaction) DeleteTenant(name KeyConvertible) error {
	tenantName := name.FDBKey()
	if len(tenantName) == 0 || tenantName[0] == '\xff' {
		return errTenantNameInvalid
	}
	if err := t.Options().SetSpecialKeySpaceEnableWrites(); err != nil {
		return err
	}

	key := append(Key(tenantMapKey), name.FDBKey()...)

	exist, err := t.checkTenantExist(key)
	if err != nil {
		return err
	}
	if !exist {
		return errTenantNotFound
	}

	t.Clear(key)

	return nil
}

// DeleteTenant deletes an existing tenant in the cluster. If the provided tenant name doesn't exist an error will be thrown.
func (d Database) DeleteTenant(name KeyConvertible) error {
	_, err := d.Transact(func(t Transaction) (interface{}, error) {
		return nil, t.DeleteTenant(name)
	})
	return err
}

// DeleteTenantsIfExist deletes tenants in the cluster.
// If a tenant name does not already exist, or is illegal or empty, this
// is not considered an error; a nil error is still returned
// from DeleteTenantsIfExist().
// Errors communicating with the database are, of course, still reported.
func (d Database) DeleteTenantsIfExist(names []KeyConvertible) error {
	_, err := d.Transact(func(t Transaction) (interface{}, error) {

		if err := t.Options().SetSpecialKeySpaceEnableWrites(); err != nil {
			return nil, err
		}
		for _, name := range names {
			tenantName := name.FDBKey()
			if len(tenantName) == 0 || tenantName[0] == '\xff' {
				continue
			}
			key := append(Key(tenantMapKey), tenantName...)
			t.Clear(key)
		}
		return nil, nil
	})
	return err
}

// ListTenants lists all existings tenants in the cluster.
func (t Transaction) ListTenants() ([]Key, error) {
	if err := t.Options().SetSpecialKeySpaceEnableWrites(); err != nil {
		return nil, err
	}

	kr, err := PrefixRange(Key(tenantMapKey))
	if err != nil {
		return nil, err
	}

	rawTenants, err := t.GetRange(kr, RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, err
	}

	// trim tenant prefix
	tenants := make([]Key, len(rawTenants))
	for i, rt := range rawTenants {
		tenants[i] = rt.Key[len(tenantMapKey):]
	}

	return tenants, nil
}

// TenantExists returns true if the tenantName is already
// present as a tenant in the Database d. If an error is
// encountered during the check, then exists will be false
// and err will be set. So always check err
// before handling the exists response.
func (d Database) TenantExists(tenantName KeyConvertible) (exists bool, err error) {

	key := append(Key(tenantMapKey), tenantName.FDBKey()...)

	_, err = d.Transact(func(t Transaction) (interface{}, error) {
		exists, err = t.checkTenantExist(key)
		return false, err
	})
	if err != nil {
		return false, err
	}
	return
}

// ListTenants lists all existings tenants in the cluster.
func (d Database) ListTenants() ([]Key, error) {
	tenants, err := d.Transact(func(t Transaction) (interface{}, error) {
		return t.ListTenants()
	})
	if err != nil {
		return nil, err
	}
	return tenants.([]Key), err
}

func (t Transaction) checkTenantExist(tenantPath Key) (bool, error) {
	buf, err := t.Get(tenantPath).Get()
	if err != nil {
		return false, err
	}

	return len(buf) > 0, nil
}

// Tenant is a handle to a FoundationDB tenant. Tenant is a lightweight
// object that may be efficiently copied, and is safe for concurrent use by
// multiple goroutines.
type Tenant struct {
	*tenant

	db Database
}

// Close releases the local handle/resources associated
// with the tenant; it does not affect the
// tenant's existence or the data it owns at all.
// Client programs should arrange to call Close()
// when they are done with the Tenant. Often
// this can be done conveniently in a defer statement
// right after OpenTenant. Close is idempotent and
// goroutine safe. It is safe to call Close()
// multiple times or from multiple goroutines.
func (t *Tenant) Close() {
	t.destroy()
}

type tenant struct {
	// Remember that sync.Mutex cannot be copied.
	// Therefore, mut must live in a pointed-to
	// struct such as tenant, rather than a passed
	// by value struct such as Tenant.
	mut  sync.Mutex
	gone bool
	ptr  *C.FDBTenant
}

// OpenTenant returns a tenant handle identified by the given name. All transactions
// created by this tenant will operate on the tenant’s key-space.
// OpenTenant will return an error if the name is not
// an existing tenant. Prefer EnsureTenant() if you are not sure.
func (d Database) OpenTenant(name KeyConvertible) (Tenant, error) {

	exists, err := d.TenantExists(name)
	if err != nil {
		return Tenant{}, err
	}
	if !exists {
		return Tenant{}, errTenantNotFound
	}

	var outt *C.FDBTenant
	if err := C.fdb_database_open_tenant(d.database.ptr, byteSliceToPtr(name.FDBKey()), C.int(len(name.FDBKey())), &outt); err != 0 {
		return Tenant{}, Error{int(err)}
	}

	tnt := &tenant{ptr: outt}
	// Do not use finalizers, they cause crashes!
	// Have the user call Close() instead.

	return Tenant{tenant: tnt, db: d}, nil
}

func (t *tenant) destroy() {
	// only call C.fdb_tenant_destroy once.
	t.mut.Lock()
	defer t.mut.Unlock()
	if t.gone {
		return
	}
	t.gone = true

	if t.ptr == nil {
		return
	}
	C.fdb_tenant_destroy(t.ptr)
}

// CreateTransaction returns a new FoundationDB transaction. It is generally
// preferable to use the (Tenant).Transact method, which handles
// automatically creating and committing a transaction with appropriate retry
// behavior.
func (t Tenant) CreateTransaction() (Transaction, error) {
	var outt *C.FDBTransaction

	if err := C.fdb_tenant_create_transaction(t.ptr, &outt); err != 0 {
		return Transaction{}, Error{int(err)}
	}

	trx := &transaction{outt, t.db}
	runtime.SetFinalizer(trx, (*transaction).destroy)

	return Transaction{trx}, nil
}

// Transact runs a caller-provided function inside a retry loop, providing it
// with a newly created Transaction. After the function returns, the Transaction
// will be committed automatically. Any error during execution of the function
// (by panic or return) or the commit will cause the function and commit to be
// retried or, if fatal, return the error to the caller.
//
// When working with Future objects in a transactional function, you may either
// explicitly check and return error values using Get, or call MustGet. Transact
// will recover a panicked Error and either retry the transaction or return the
// error.
//
// The transaction is retried if the error is or wraps a retryable Error.
// The error is unwrapped.
//
// Do not return Future objects from the function provided to Transact. The
// Transaction created by Transact may be finalized at any point after Transact
// returns, resulting in the cancellation of any outstanding
// reads. Additionally, any errors returned or panicked by the Future will no
// longer be able to trigger a retry of the caller-provided function.
//
// See the Transactor interface for an example of using Transact with
// Transaction and Database objects.
func (t Tenant) Transact(f func(Transaction) (interface{}, error)) (interface{}, error) {
	tr, e := t.CreateTransaction()
	// Any error here is non-retryable
	if e != nil {
		return nil, e
	}

	wrapped := func() (ret interface{}, e error) {
		defer panicToError(&e)

		ret, e = f(tr)

		if e == nil {
			e = tr.Commit().Get()
		}

		return
	}

	return retryable(wrapped, tr.OnError)
}

// ReadTransact runs a caller-provided function inside a retry loop, providing
// it with a newly created Transaction (as a ReadTransaction). Any error during
// execution of the function (by panic or return) will cause the function to be
// retried or, if fatal, return the error to the caller.
//
// When working with Future objects in a read-only transactional function, you
// may either explicitly check and return error values using Get, or call
// MustGet. ReadTransact will recover a panicked Error and either retry the
// transaction or return the error.
//
// The transaction is retried if the error is or wraps a retryable Error.
// The error is unwrapped.
//
// Do not return Future objects from the function provided to ReadTransact. The
// Transaction created by ReadTransact may be finalized at any point after
// ReadTransact returns, resulting in the cancellation of any outstanding
// reads. Additionally, any errors returned or panicked by the Future will no
// longer be able to trigger a retry of the caller-provided function.
//
// See the ReadTransactor interface for an example of using ReadTransact with
// Transaction, Snapshot and Database objects.
func (t Tenant) ReadTransact(f func(ReadTransaction) (interface{}, error)) (interface{}, error) {
	tr, e := t.CreateTransaction()
	// Any error here is non-retryable
	if e != nil {
		return nil, e
	}

	wrapped := func() (ret interface{}, e error) {
		defer panicToError(&e)

		ret, e = f(tr)

		if e == nil {
			e = tr.Commit().Get()
		}

		return
	}

	return retryable(wrapped, tr.OnError)
}
