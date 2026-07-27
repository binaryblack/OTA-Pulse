// Copyright 2023 Northern.tech AS
//
//	Licensed under the Apache License, Version 2.0 (the "License");
//	you may not use this file except in compliance with the License.
//	You may obtain a copy of the License at
//
//	    http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS,
//	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	See the License for the specific language governing permissions and
//	limitations under the License.
package inventory

import (
	"io/ioutil"
	"os"
	"path"
	"testing"
	"time"

	"github.com/binaryblack/OTA-Pulse/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInventoryDataDecoder(t *testing.T) {

	idec := NewInventoryDataDecoder()
	assert.NotNil(t, idec)

	idec.AppendFromRaw(map[string][]string{
		"foo": []string{"bar"},
	})

	assert.Contains(t, idec.GetInventoryData(), client.InventoryAttribute{
		Name:  "foo",
		Value: "bar",
	})

	idec.AppendFromRaw(map[string][]string{
		"foo": []string{"baz"},
	})
	assert.Contains(t, idec.data, "foo")
	assert.Contains(t, idec.GetInventoryData(),
		client.InventoryAttribute{
			Name:  "foo",
			Value: []string{"bar", "baz"}})

	idec.AppendFromRaw(map[string][]string{
		"bar": []string{"zen"},
	})
	assert.Contains(t, idec.GetInventoryData(),
		client.InventoryAttribute{
			Name:  "foo",
			Value: []string{"bar", "baz"}})
	assert.Contains(t, idec.GetInventoryData(), client.InventoryAttribute{
		Name:  "bar",
		Value: "zen",
	})

	idata := idec.GetInventoryData()
	assert.Len(t, idata, 2)
	assert.Contains(t, idata, client.InventoryAttribute{
		Name:  "foo",
		Value: []string{"bar", "baz"}})
	assert.Contains(t, idata, client.InventoryAttribute{
		Name:  "bar",
		Value: "zen"})
}

func TestInventoryDataParseError(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	fd, err := os.OpenFile(path.Join(tmpDir, "mender-inventory-test"),
		os.O_CREATE|os.O_WRONLY, 0755)
	require.NoError(t, err)
	fd.Write([]byte("#!/bin/sh\necho bogus\n"))
	fd.Close()

	inventory := NewInventoryDataRunner(tmpDir)
	data, err := inventory.Get()
	// Does not return individial errors, only logging, but should result in
	// empty inventory data.
	assert.NoError(t, err)
	assert.Equal(t, 0, len(data))
}

func TestInventoryDataHungToolIsKilled(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// a well-behaved tool that returns quickly
	fastFd, err := os.OpenFile(path.Join(tmpDir, "mender-inventory-fast"),
		os.O_CREATE|os.O_WRONLY, 0755)
	require.NoError(t, err)
	fastFd.Write([]byte("#!/bin/sh\necho key=value\n"))
	fastFd.Close()

	// a tool that hangs forever - simulates GAP-OTA-013
	hangFd, err := os.OpenFile(path.Join(tmpDir, "mender-inventory-hang"),
		os.O_CREATE|os.O_WRONLY, 0755)
	require.NoError(t, err)
	hangFd.Write([]byte("#!/bin/sh\nsleep 3600\n"))
	hangFd.Close()

	origTimeout := inventoryToolTimeout
	inventoryToolTimeout = 200 * time.Millisecond
	defer func() { inventoryToolTimeout = origTimeout }()

	inventory := NewInventoryDataRunner(tmpDir)

	done := make(chan struct{})
	var data client.InventoryData
	go func() {
		data, err = inventory.Get()
		close(done)
	}()

	select {
	case <-done:
		// the hung tool must be killed within the shortened timeout instead
		// of stalling Get() indefinitely
		assert.NoError(t, err)
		assert.Contains(t, data, client.InventoryAttribute{Name: "key", Value: "value"})
	case <-time.After(5 * time.Second):
		t.Fatal("inventory.Get() did not return - hung tool was not killed")
	}
}
