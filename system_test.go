package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSHKey(t *testing.T) {
	assert := assert.New(t)

	tempdir, _ := ioutil.TempDir("", "ddc")
	defer os.RemoveAll(tempdir)

	sc, _ := NewSystemClientWithBase(tempdir)

	key1, err := sc.EnsureSSHKey()
	assert.Nil(err)
	assert.NotEmpty(key1.privatePath)
	assert.NotEmpty(key1.publicPath)
	fmt.Println(key1.privatePath)

	key2, err := sc.EnsureSSHKey()
	assert.Nil(err)

	assert.Equal(key1.privatePath, key2.privatePath)
	assert.Equal(key1.publicPath, key2.publicPath)
}

func TestDir(t *testing.T) {
	assert := assert.New(t)

	tempdir, _ := ioutil.TempDir("", "ddc")
	defer os.RemoveAll(tempdir)

	sc, _ := NewSystemClientWithBase(tempdir)

	path, err := sc.EnsureEnvironmentDir("foo")
	assert.Nil(err)
	assert.NotEmpty(path)
}
