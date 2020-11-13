// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package migration

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3" //ignore lint
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// DoMigrate does migration on provided database
func DoMigrate(sql *sql.DB, migrationPath string, migrateVersion uint) (err error) {
	log.Debugf("Db Migration start migration to %d", int(migrateVersion))

	m, err := getMigrationFromInstance(sql, migrationPath)
	if err != nil {
		return err
	}

	version, dirty, err := m.Version()
	if err == migrate.ErrNilVersion {
		version = 0

		log.Debugf("Migration version was not set. Setting initial version %d", version)

		if err = m.Force(int(version)); err != nil {
			return fmt.Errorf("unable to force version. err: %s", err)
		}
	} else if err != nil {
		return fmt.Errorf("unable to get migration version: %s", err)
	}

	if dirty == true {
		return errors.New("can't update, db is dirty")
	}

	log.Debugf("Got database version: %d", version)

	if version == migrateVersion {
		log.Debugf("No migration needed. db version is: %d", int(migrateVersion))
		return nil
	}

	err = m.Migrate(migrateVersion)
	if err == migrate.ErrNoChange {
		log.Debugf("No migration needed. db version is: %d", int(migrateVersion))
		return nil
	}

	if err == nil {
		log.Debugf("Migration successful, db version is: %d", int(migrateVersion))
	}

	return err
}

// SetDatabaseVersion sets the database version
func SetDatabaseVersion(sql *sql.DB, migrationPath string, version uint) (err error) {
	m, err := getMigrationFromInstance(sql, migrationPath)
	if err != nil {
		return err
	}

	if err = m.Force(int(version)); err != nil {
		return fmt.Errorf("unable to force version. err: %s", err)
	}

	return nil
}

// MergeMigrationFiles merged the migration files with the previous state
func MergeMigrationFiles(migrationPath string, mergedMigrationPath string) (err error) {
	absMigrationPath, err := filepath.Abs(migrationPath)
	if err != nil {
		return fmt.Errorf("can't get absolute migration path: %s", err)
	}

	absMergedMigrationPath, err := filepath.Abs(mergedMigrationPath)
	if err != nil {
		return fmt.Errorf("can't get absolute merged migration path: %s", err)
	}

	if _, err = os.Stat(absMigrationPath); err != nil {
		return fmt.Errorf("migration path doesn't exists %s", err)
	}

	//Create target directories if needed
	if err = os.MkdirAll(absMergedMigrationPath, 0755); err != nil {
		return err
	}

	if err = copyFiles(absMigrationPath, absMergedMigrationPath); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func copyFiles(source, destination string) (err error) {
	err = filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		var relPath string = strings.Replace(path, source, "", 1)
		if relPath == "" {
			return nil
		}

		if _, err := os.Stat(filepath.Join(destination, relPath)); err == nil {
			//File exists on destination
			return nil
		}

		if info.IsDir() {
			return os.Mkdir(filepath.Join(destination, relPath), 0755)
		}

		return copyFile(filepath.Join(source, relPath), filepath.Join(destination, relPath))
	})

	return err
}

func copyFile(src string, dst string) (err error) {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}

	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()
	_, err = io.Copy(destination, source)
	return err
}

func getMigrationFromInstance(sql *sql.DB, mergedMigrationPath string) (migration *migrate.Migrate, err error) {
	absMergedMigrationPath, err := filepath.Abs(mergedMigrationPath)
	if err != nil {
		return nil, fmt.Errorf("can't get absolute merged migration path: %s", err)
	}

	driver, err := sqlite3.WithInstance(sql, &sqlite3.Config{})
	if err != nil {
		return nil, fmt.Errorf("error creating migration driver, err: %s", err)
	}

	m, err := migrate.NewWithDatabaseInstance(
		"file://"+absMergedMigrationPath,
		"sqlite3", driver)
	if err != nil {
		return nil, err
	}

	return m, nil
}