// +build !oss

/*
 * Copyright 2021 Dgraph Labs, Inc. and Contributors
 *
 * Licensed under the Dgraph Community License (the "License"); you
 * may not use this file except in compliance with the License. You
 * may obtain a copy of the License at
 *
 *     https://github.com/dgraph-io/dgraph/blob/master/licenses/DCL.txt
 */

package audit

import (
	"context"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/dgraph/x"
	"github.com/golang/glog"
)

var auditEnabled uint32

type AuditEvent struct {
	User        string
	ServerHost  string
	ClientHost  string
	Endpoint    string
	ReqType     string
	Req         string
	Status      string
	QueryParams map[string][]string
}

const (
	UnauthorisedUser = "UnauthorisedUser"
	UnknownUser      = "UnknownUser"
	PoorManAuth      = "PoorManAuth"
	Grpc             = "Grpc"
	Http             = "Http"
)

var auditor *auditLogger = &auditLogger{}

type auditLogger struct {
	log  *x.Logger
	tick *time.Ticker
}

func ReadAuditEncKey(conf string) ([]byte, error) {
	encFile := x.GetFlagString(conf, "encrypt-file")
	if encFile == "" {
		return nil, nil
	}
	path, err := filepath.Abs(encFile)
	if err != nil {
		return nil, err
	}
	encKey, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return encKey, nil
}

// InitAuditorIfNecessary accepts conf and enterprise edition check function.
// This method keep tracks whether cluster is part of enterprise edition or not.
// It pools eeEnabled function every five minutes to check if the license is still valid or not.
func InitAuditorIfNecessary(conf string, eeEnabled func() bool) {
	if conf == "" {
		return
	}
	encKey, err := ReadAuditEncKey(conf)
	if err != nil {
		glog.Errorf("error while reading encryption file", err)
		return
	}
	if eeEnabled() {
		InitAuditor(x.GetFlagString(conf, "dir"), encKey)
	}
	auditor.tick = time.NewTicker(time.Minute * 5)
	go trackIfEEValid(x.GetFlagString(conf, "dir"), encKey, eeEnabled)
}

// InitAuditor initializes the auditor.
// This method doesnt keep track of whether cluster is part of enterprise edition or not.
// Client has to keep track of that.
func InitAuditor(dir string, key []byte) {
	auditor.log = initlog(dir, key)
	atomic.StoreUint32(&auditEnabled, 1)
	glog.Infoln("audit logs are enabled")
}

func initlog(dir string, key []byte) *x.Logger {
	logger, err := x.InitLogger(dir, "dgraph_audit.log", key)
	if err != nil {
		glog.Errorf("error while initiating auditor %v", err)
		return nil
	}
	return logger
}

// trackIfEEValid tracks enterprise license of the cluster.
// Right now alpha doesn't know about the enterprise/licence.
// That's why we needed to track if the current node is part of enterprise edition cluster
func trackIfEEValid(dir string, key []byte, eeEnabledFunc func() bool) {
	for {
		select {
		case <-auditor.tick.C:
			if !eeEnabledFunc() && atomic.CompareAndSwapUint32(&auditEnabled, 1, 0) {
				glog.Infof("audit logs are disabled")
				auditor.log.Sync()
				auditor.log = nil
				continue
			}

			if atomic.LoadUint32(&auditEnabled) != 1 {
				auditor.log = initlog(dir, key)
				atomic.StoreUint32(&auditEnabled, 1)
				glog.Infof("audit logs are enabled")
			}
		}
	}
}

// Close stops the ticker and sync the pending logs in buffer.
// It also sets the log to nil, because its being called by zero when license expires.
// If license added, InitLogger will take care of the file.
func Close() {
	if auditor.tick != nil {
		auditor.tick.Stop()
	}
	auditor.log.Sync()
	auditor.log = nil
}

func (a *auditLogger) Audit(event *AuditEvent) {
	a.log.AuditI(event.Endpoint,
		"user", event.User,
		"server", event.ServerHost,
		"client", event.ClientHost,
		"req_type", event.ReqType,
		"req_body", event.Req,
		"query_param", event.QueryParams,
		"status", event.Status)
}

func auditGrpc(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, err error) {
	clientHost := ""
	if p, ok := peer.FromContext(ctx); ok {
		clientHost = p.Addr.String()
	}

	userId := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if t := md.Get("accessJwt"); len(t) > 0 {
			userId = getUserId(t[0], false)
		} else if t := md.Get("auth-token"); len(t) > 0 {
			userId = getUserId(t[0], true)
		}
	}

	cd := codes.Unknown
	if serr, ok := status.FromError(err); ok {
		cd = serr.Code()
	}
	auditor.Audit(&AuditEvent{
		User:       userId,
		ServerHost: x.WorkerConfig.MyAddr,
		ClientHost: clientHost,
		Endpoint:   info.FullMethod,
		ReqType:    Grpc,
		Req:        fmt.Sprintf("%+v", req),
		Status:     cd.String(),
	})
}

func auditHttp(w *ResponseWriter, r *http.Request) {
	rb, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rb = []byte(err.Error())
	}

	userId := ""
	if token := r.Header.Get("X-Dgraph-AccessToken"); token != "" {
		userId = getUserId(token, false)
	} else if token := r.Header.Get("X-Dgraph-AuthToken"); token != "" {
		userId = getUserId(token, true)
	} else {
		userId = getUserId("", false)
	}
	auditor.Audit(&AuditEvent{
		User:        userId,
		ServerHost:  x.WorkerConfig.MyAddr,
		ClientHost:  r.RemoteAddr,
		Endpoint:    r.URL.Path,
		ReqType:     Http,
		Req:         string(rb),
		Status:      http.StatusText(w.statusCode),
		QueryParams: r.URL.Query(),
	})
}

func getUserId(token string, poorman bool) string {
	if poorman {
		return PoorManAuth
	}
	var userId string
	var err error
	if token == "" {
		if x.WorkerConfig.AclEnabled {
			userId = UnauthorisedUser
		}
	} else {
		if userId, err = x.ExtractUserName(token); err != nil {
			userId = UnknownUser
		}
	}
	return userId
}