// Copyright 2021 eatMoreApple.  All rights reserved.
// Use of this source code is governed by a GPL style
// license that can be found in the LICENSE file.

package regia

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultMultipartMemory = 32 << 20

type Context struct {
	Request        *http.Request
	ResponseWriter http.ResponseWriter
	index          uint8
	abortIndex     uint8
	group          HandleFuncGroup
	// Mat multipart form memory size
	// default 32M
	MultipartMemory int64
	// context data carrier
	contextValue *SyncMap
	Engine       *Engine
	FileStorage  FileStorage
	Parsers      Parsers
	Validator    Validator
	Params       Params
	abort        Exit
	// if url matched
	matched bool
	// escape is a flag decide if is return to the context pool
	escape bool

	// query cache
	queryCache url.Values
	// form cache
	formCache url.Values
}

// init prepare for this request
func (c *Context) init(req *http.Request, writer http.ResponseWriter, params Params, group HandleFuncGroup) {
	c.Request = req
	c.ResponseWriter = writer
	c.Params = params
	c.group = group
	c.abort = c.Engine.Abort
	c.FileStorage = c.Engine.FileStorage
	c.MultipartMemory = c.Engine.MultipartMemory
	c.Validator = c.Engine.ContextValidator
}

// reset current Context
func (c *Context) reset() {
	c.index = 0
	c.Parsers = nil
	c.matched = false
	c.queryCache = nil
	c.formCache = nil
	if c.contextValue != nil {
		// clear and return to the syncMapPool
		c.contextValue.Clear()
		syncMapPool.Put(c.contextValue)
		c.contextValue = nil
	}
}

// start to handle current request
func (c *Context) start() {
	defer c.recover()
	c.Next()
}

// I do not think it is a good design
func (c *Context) recover() {
	if rec := recover(); rec != nil {
		if e, ok := rec.(Exit); ok {
			e.Exit(c)
		} else {
			panic(rec)
		}
	}
}

// IsMatched return that route matched
func (c *Context) IsMatched() bool {
	return c.matched
}

// Next call handle
func (c *Context) Next() {
	c.index++
	for c.index <= uint8(len(c.group)) {
		handle := c.group[c.index-1]
		handle(c)
		c.index++
	}
}

// SetAbort Set Exit for this request
func (c *Context) SetAbort(abort Exit) {
	c.abort = abort
}

// Abort skip current handle and will call Context.abort
// exit and do nothing by default
func (c *Context) Abort() { c.AbortWith(c.abort) }

// AbortWith skip current handle and call given exit
func (c *Context) AbortWith(exit Exit) {
	c.abortIndex = c.index - 1
	panic(exit)
}

// Flusher Make http.ResponseWriter as http.Flusher
func (c *Context) Flusher() http.Flusher { return c.ResponseWriter.(http.Flusher) }

// SaveUploadFile will call Context.FileStorage
// default save file to local path
func (c *Context) SaveUploadFile(name string) (string, error) {
	return c.SaveUploadFileWith(c.FileStorage, name)
}

// SaveUploadFileWith call given FileStorage with upload file
func (c *Context) SaveUploadFileWith(fs FileStorage, name string) (string, error) {
	if fs == nil {
		return "", errors.New("`FileStorage` can be nil type")
	}
	file, fileHeader, err := c.Request.FormFile(name)
	if err != nil {
		return "", err
	}
	// try to close return file
	if err = file.Close(); err != nil {
		return "", err
	}
	return c.FileStorage.Save(fileHeader)
}

// Data analysis request body to destination and validate
// Call Context.AddParser to add more support
func (c *Context) Data(v interface{}) error {
	if c.Parsers == nil {
		c.Parsers = c.Engine.ContextParser
	}
	if err := c.Parsers.Parse(c, v); err != nil {
		return err
	}
	return c.Validator.Validate(v)
}

// AddParser add more Parser for Context.Data
func (c *Context) AddParser(p ...Parser) {
	c.Parsers = append(c.Parsers, p...)
}

// ContextValue is a goroutine safe context data storage
func (c *Context) ContextValue() *SyncMap {
	if c.contextValue == nil {
		c.contextValue = syncMapPool.Get().(*SyncMap)
	}
	return c.contextValue
}

// Query is a shortcut for c.Request.URL.Query()
// but cached value for current context
func (c *Context) Query() url.Values {
	if c.queryCache == nil {
		c.queryCache = c.Request.URL.Query()
	}
	return c.queryCache
}

// QueryValue get Value from url query
func (c *Context) QueryValue(key string) Value {
	value := c.Query().Get(key)
	return Value(value)
}

// QueryValues get Value slice from url query
func (c *Context) QueryValues(key string) Values {
	values := c.Query()[key]
	return NewValues(values)
}

// Form is a shortcut for c.Request.PostForm
// but value for current context
func (c *Context) Form() url.Values {
	if c.formCache == nil {
		c.Request.ParseForm()
		c.formCache = c.Request.PostForm
	}
	return c.formCache
}

// FormValue get Value from post value
func (c *Context) FormValue(key string) Value {
	value := c.Form().Get(key)
	return Value(value)
}

// FormValues get Values slice from post value
func (c *Context) FormValues(key string) Values {
	value := c.Form()[key]
	return NewValues(value)
}

// Bind bind request to destination
func (c *Context) Bind(binder Binder, v interface{}) error {
	return binder.Bind(c, v)
}

// BindQuery bind Query to destination
func (c *Context) BindQuery(v interface{}) error {
	return c.Bind(queryBinder, v)
}

// BindForm bind PostForm to destination
func (c *Context) BindForm(v interface{}) error {
	if err := c.Request.ParseForm(); err != nil {
		return err
	}
	return c.Bind(formBinder, v)
}

// BindMultipartForm bind MultipartForm to destination
func (c *Context) BindMultipartForm(v interface{}) error {
	if err := c.Request.ParseMultipartForm(c.MultipartMemory); err != nil {
		return err
	}
	return c.Bind(multipartFormBinder, v)
}

// BindJSON bind the request body according to the format of json
func (c *Context) BindJSON(v interface{}) error {
	return c.Bind(jsonBinder, v)
}

// BindXML bind the request body according to the format of xml
func (c *Context) BindXML(v interface{}) error {
	return c.Bind(xmlBinder, v)
}

// SetStatus set response status code
// call this method at last
func (c *Context) SetStatus(code int) {
	c.ResponseWriter.WriteHeader(code)
}

// SetHeader set response header
func (c *Context) SetHeader(key, value string) {
	c.ResponseWriter.Header().Set(key, value)
}

// SetCookie is a shortcut for http.SetCookie
func (c *Context) SetCookie(cookie *http.Cookie) {
	http.SetCookie(c.ResponseWriter, cookie)
}

// Render write response data with given Render
func (c *Context) Render(render Render, data interface{}) error {
	return render.Render(c.ResponseWriter, data)
}

// JSON write json response
func (c *Context) JSON(data interface{}) error {
	return c.Render(jsonRender, data)
}

// XML write xml response
func (c *Context) XML(data interface{}) error {
	return c.Render(xmlRender, data)
}

// Text write string response
func (c *Context) Text(format string, data ...interface{}) (err error) {
	writeContentType(c.ResponseWriter, textHtmlContentType)
	if len(data) > 0 {
		_, err = fmt.Fprintf(c.ResponseWriter, format, data...)
	} else {
		_, err = c.ResponseWriter.Write(stringToByte(format))
	}
	return err
}

// Redirect Shortcut for http.Redirect
func (c *Context) Redirect(code int, url string) {
	http.Redirect(c.ResponseWriter, c.Request, url, code)
}

// ServeFile Shortcut for http.ServeFile
func (c *Context) ServeFile(path string) {
	http.ServeFile(c.ResponseWriter, c.Request, path)
}

// ServeContent Shortcut for http.ServeContent
func (c *Context) ServeContent(name string, modTime time.Time, content io.ReadSeeker) {
	http.ServeContent(c.ResponseWriter, c.Request, name, modTime, content)
}

// Escape can let context not return to the pool
func (c *Context) Escape() {
	c.escape = true
}

// IsAborted return that context is aborted
func (c *Context) IsAborted() bool {
	return c.abortIndex != 0
}

// AbortHandler returns a handler which called at lasted
func (c *Context) AbortHandler() HandleFunc {
	if !c.IsAborted() {
		return nil
	}
	return c.group[c.abortIndex]
}

// AbortWithJSON write json response and exit
func (c *Context) AbortWithJSON(data interface{}) {
	_ = c.JSON(data)
	c.Abort()
}

// AbortWithXML write xml response and exit
func (c *Context) AbortWithXML(data interface{}) {
	_ = c.XML(data)
	c.Abort()
}

// AbortWithText write string response and exit
func (c *Context) AbortWithText(text string) {
	_ = c.Text(text)
	c.Abort()
}

// IsWebsocket returns true if the request headers indicate that a websocket
func (c *Context) IsWebsocket() bool {
	return strings.Contains(strings.ToLower(c.Request.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(c.Request.Header.Get("Upgrade"), "websocket")
}

// Write []byte into response writer
func (c *Context) Write(data []byte) error {
	_, err := c.ResponseWriter.Write(data)
	return err
}
