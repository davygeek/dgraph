/*
 * Copyright 2021 Dgraph Labs, Inc. and Contributors
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

package x

import (
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func InitLogger(dir string, filename string, key []byte, compress bool) (*Logger, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if key != nil {
		filename = filename + ".enc"
	}

	path, err := filepath.Abs(filepath.Join(dir, filename))
	if err != nil {
		return nil, err
	}
	w := &LogWriter{
		FilePath:      path,
		MaxSize:       100,
		MaxAge:        30,
		EncryptionKey: key,
		Compress:      compress,
	}
	if w, err = w.Init(); err != nil {
		return nil, err
	}
	return &Logger{
		logger: zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
			zapcore.AddSync(w), zap.DebugLevel)),
	}, nil
}

type Logger struct {
	logger *zap.Logger
}

// AuditI logs audit message as info. args are key value pairs with key as string value
func (l *Logger) AuditI(msg string, args ...interface{}) {
	if l == nil {
		return
	}
	flds := make([]zap.Field, 0)
	for i := 0; i < len(args); i = i + 2 {
		flds = append(flds, zap.Any(args[i].(string), args[i+1]))
	}
	l.logger.Info(msg, flds...)
}

func (l *Logger) AuditE(msg string, args ...interface{}) {
	if l == nil {
		return
	}
	flds := make([]zap.Field, 0)
	for i := 0; i < len(args); i = i + 2 {
		flds = append(flds, zap.Any(args[i].(string), args[i+1]))
	}
	l.logger.Error(msg, flds...)
}

func (l *Logger) Sync() {
	if l == nil {
		return
	}
	_ = l.logger.Sync()
}
