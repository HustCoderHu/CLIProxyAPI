package cliproxy

import (
	"context"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// qoderModelRefreshRequest captures publication ownership at admission. A queued
// successor must not adopt a later epoch from an external publisher.
type qoderModelRefreshRequest struct {
	ctx           context.Context
	auth          *coreauth.Auth
	provider      string
	registryEpoch uint64
}

type qoderCatalogPersistRequest struct {
	ctx      context.Context
	auth     *coreauth.Auth
	routeKey string
	endpoint string
	catalog  *sdkauth.QoderCatalog
	cancel   context.CancelFunc
}

// queueQoderModelRefresh coalesces registration and periodic probes into one
// worker per account. Repeated updates replace a single queued successor rather
// than accumulating singleflight followers while an upstream is stalled.
func (s *Service) queueQoderModelRefresh(ctx context.Context, auth *coreauth.Auth, provider string) {
	if s == nil || auth == nil || auth.ID == "" || auth.Disabled || s.qoderHomeEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, auth.Provider) {
		return
	}
	s.cfgMu.RLock()
	lifetime := s.qoderContext
	s.cfgMu.RUnlock()
	if lifetime != nil && lifetime.Err() != nil {
		return
	}
	request := &qoderModelRefreshRequest{
		ctx: ctx, auth: auth.Clone(), provider: provider,
		registryEpoch: GlobalModelRegistry().ClientRegistrationEpoch(auth.ID),
	}
	probe := &qoderAccountProbe{
		auth: request.auth, routeKey: s.qoderCatalogKey(request.auth),
		token: qoderCatalogAuthString(request.auth, "access_token"),
	}
	s.qoderProbeMu.Lock()
	// Late stale callers must not replace the latest queued auth snapshot.
	if ctx.Err() != nil || !s.qoderAccountProbeCurrent(probe) {
		s.qoderProbeMu.Unlock()
		return
	}
	if s.qoderRefreshPending == nil {
		s.qoderRefreshPending = make(map[string]*qoderModelRefreshRequest)
	}
	queued, running := s.qoderRefreshPending[auth.ID]
	if queued != nil && queued.registryEpoch > request.registryEpoch {
		// Concurrent registration callers can reach admission out of order.
		s.qoderProbeMu.Unlock()
		return
	}
	s.qoderRefreshPending[auth.ID] = request
	if !running {
		s.qoderProbeWg.Add(1)
	}
	s.qoderProbeMu.Unlock()
	if !running {
		go s.runQoderAccountRefresh(auth.ID)
	}
}

func (s *Service) runQoderAccountRefresh(authID string) {
	defer s.qoderProbeWg.Done()
	defer s.signalQoderModelRefresh()
	for {
		s.qoderProbeMu.Lock()
		request := s.qoderRefreshPending[authID]
		if request == nil {
			delete(s.qoderRefreshPending, authID)
			s.qoderProbeMu.Unlock()
			return
		}
		s.qoderRefreshPending[authID] = nil
		s.qoderProbeMu.Unlock()
		s.executeQoderModelRefresh(request)
	}
}

func (s *Service) executeQoderModelRefresh(request *qoderModelRefreshRequest) {
	if request.ctx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, request.auth.Provider) {
		return
	}
	probeCtx, cancel := context.WithCancel(request.ctx)
	defer cancel()
	s.cfgMu.RLock()
	lifetime := s.qoderContext
	s.cfgMu.RUnlock()
	if lifetime != nil {
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		if lifetime.Err() != nil {
			return
		}
	}
	expectedKey, catalog := s.probeAndRegisterQoderModels(probeCtx, request.auth, request.provider, request.auth.RegistrationEpoch, request.registryEpoch)
	if catalog != nil {
		s.queueQoderCatalogPersistence(request.ctx, request.auth, expectedKey, qoderGatewayBaseURLs(request.auth)[0], catalog)
	}
}

// qoderAccountProbe owns both queued and connected work for one auth snapshot.
// Cancellation releases network slots without imposing an upstream timeout.
type qoderAccountProbe struct {
	auth      *coreauth.Auth
	routeKey  string
	flightKey string
	token     string
	cancel    context.CancelFunc
}

func (s *Service) beginQoderAccountProbe(parent context.Context, auth *coreauth.Auth, routeKey, flightKey, token string) (context.Context, func(), bool) {
	ctx, cancel := context.WithCancel(parent)
	s.cfgMu.RLock()
	lifetime := s.qoderContext
	s.cfgMu.RUnlock()
	var stop func() bool
	if lifetime != nil {
		stop = context.AfterFunc(lifetime, cancel)
		if lifetime.Err() != nil {
			cancel()
		}
	}
	var changes <-chan struct{}
	var unsubscribe func()
	if s.coreManager != nil {
		// Subscribe before validation, so a native refresh or removal cannot fall
		// between checking the auth and installing the invalidation observer.
		changes, unsubscribe = s.coreManager.WatchAuthChanges(auth.ID)
	}
	var watcherDone chan struct{}
	probe := &qoderAccountProbe{auth: auth.Clone(), routeKey: routeKey, flightKey: flightKey, token: token, cancel: cancel}
	s.qoderProbeMu.Lock()
	valid := ctx.Err() == nil && s.qoderAccountProbeCurrent(probe)
	if valid {
		if s.qoderAccountProbes == nil {
			s.qoderAccountProbes = make(map[string]map[*qoderAccountProbe]struct{})
		}
		if s.qoderAccountProbes[auth.ID] == nil {
			s.qoderAccountProbes[auth.ID] = make(map[*qoderAccountProbe]struct{})
		}
		s.qoderAccountProbes[auth.ID][probe] = struct{}{}
	}
	s.qoderProbeMu.Unlock()
	if valid && changes != nil {
		watcherDone = make(chan struct{})
		go func() {
			defer close(watcherDone)
			for {
				select {
				case <-ctx.Done():
					return
				case _, open := <-changes:
					if !open {
						return
					}
					s.cancelStaleQoderProbes(auth.ID)
				}
			}
		}()
	}
	cleanup := func() {
		cancel()
		if unsubscribe != nil {
			unsubscribe()
		}
		if watcherDone != nil {
			<-watcherDone
		}
		if stop != nil {
			stop()
		}
		s.qoderProbeMu.Lock()
		delete(s.qoderAccountProbes[auth.ID], probe)
		if len(s.qoderAccountProbes[auth.ID]) == 0 {
			delete(s.qoderAccountProbes, auth.ID)
		}
		s.qoderProbeMu.Unlock()
		s.signalQoderModelRefresh()
	}
	if !valid {
		cleanup()
		return ctx, func() {}, false
	}
	return ctx, cleanup, true
}

// Caller holds qoderProbeMu, making tracking and invalidation indivisible.
func (s *Service) qoderAccountProbeCurrent(probe *qoderAccountProbe) bool {
	if s.qoderHomeEnabled() || probe == nil || probe.auth == nil {
		return false
	}
	current := probe.auth
	if s.coreManager != nil {
		var exists bool
		current, exists = s.coreManager.GetByID(probe.auth.ID)
		if !exists || current == nil {
			return false
		}
	}
	return !current.Disabled && current.Status != coreauth.StatusDisabled &&
		qoderAuthModelSettingsEqual(current, probe.auth) &&
		s.qoderCatalogKey(current) == probe.routeKey &&
		qoderCatalogAuthString(current, "access_token") == probe.token
}

// queueQoderCatalogPersistence runs after the manager/config mutation. New
// probes check current state while joining this same map, so deletion cannot
// slip between validation and tracking. An empty authID invalidates stale
// persistence across all accounts after config changes.
func (s *Service) queueQoderCatalogPersistence(ctx context.Context, auth *coreauth.Auth, expectedKey, endpoint string, catalog *sdkauth.QoderCatalog) {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" || auth.Disabled || expectedKey == "" || catalog == nil || catalog.Revision == 0 || s.qoderHomeEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, auth.Provider) {
		return
	}
	s.cfgMu.RLock()
	lifetime := s.qoderContext
	s.cfgMu.RUnlock()
	if lifetime != nil && lifetime.Err() != nil {
		return
	}
	qoderCatalogMu.RLock()
	entry := qoderCatalogCache[expectedKey]
	stale := entry.catalog == nil || entry.catalog.Revision != catalog.Revision || entry.persistedRevision >= catalog.Revision
	qoderCatalogMu.RUnlock()
	if stale {
		return
	}
	request := &qoderCatalogPersistRequest{
		ctx:      ctx,
		auth:     auth.Clone(),
		routeKey: expectedKey,
		endpoint: endpoint,
		catalog:  cloneQoderCatalog(catalog),
	}
	s.qoderProbeMu.Lock()
	if ctx.Err() != nil || !s.qoderCatalogPersistCurrent(request) {
		s.qoderProbeMu.Unlock()
		return
	}
	if s.qoderPersistPending == nil {
		s.qoderPersistPending = make(map[string]*qoderCatalogPersistRequest)
	}
	queued, running := s.qoderPersistPending[auth.ID]
	if queued != nil && queued.catalog.Revision > request.catalog.Revision {
		s.qoderProbeMu.Unlock()
		return
	}
	if active := s.qoderPersistActive[auth.ID]; active != nil && (active.catalog.Revision < request.catalog.Revision || active.routeKey != request.routeKey) {
		if active.cancel != nil {
			active.cancel()
		}
	}
	s.qoderPersistPending[auth.ID] = request
	if !running {
		s.qoderProbeWg.Add(1)
	}
	s.qoderProbeMu.Unlock()
	if !running {
		go s.runQoderCatalogPersistence(auth.ID)
	}
}

func (s *Service) runQoderCatalogPersistence(authID string) {
	defer s.qoderProbeWg.Done()
	defer s.signalQoderModelRefresh()
	for {
		s.qoderProbeMu.Lock()
		request := s.qoderPersistPending[authID]
		if request == nil {
			delete(s.qoderPersistPending, authID)
			delete(s.qoderPersistActive, authID)
			s.qoderProbeMu.Unlock()
			return
		}
		s.qoderPersistPending[authID] = nil
		persistCtx, cancel := context.WithCancel(request.ctx)
		request.cancel = cancel
		if s.qoderPersistActive == nil {
			s.qoderPersistActive = make(map[string]*qoderCatalogPersistRequest)
		}
		s.qoderPersistActive[authID] = request
		s.qoderProbeMu.Unlock()
		s.executeQoderCatalogPersistence(persistCtx, cancel, request)
	}
}

func (s *Service) executeQoderCatalogPersistence(persistCtx context.Context, cancel context.CancelFunc, request *qoderCatalogPersistRequest) {
	defer cancel()
	if persistCtx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, request.auth.Provider) {
		return
	}
	s.cfgMu.RLock()
	lifetime := s.qoderContext
	s.cfgMu.RUnlock()
	if lifetime != nil {
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		if lifetime.Err() != nil {
			return
		}
	}
	if s.persistQoderCatalogHints(persistCtx, request.auth, request.routeKey, request.endpoint, request.catalog) && request.ctx.Err() == nil {
		s.queueQoderCatalogPersistence(request.ctx, request.auth, request.routeKey, request.endpoint, request.catalog)
	}
}

func (s *Service) qoderCatalogPersistCurrent(request *qoderCatalogPersistRequest) bool {
	if request == nil || request.auth == nil || request.catalog == nil || s.qoderHomeEnabled() {
		return false
	}
	current := request.auth
	if s.coreManager != nil {
		var exists bool
		current, exists = s.coreManager.GetByID(request.auth.ID)
		if !exists || current == nil {
			return false
		}
	}
	return !current.Disabled && current.Status != coreauth.StatusDisabled &&
		current.Provider == request.auth.Provider &&
		current.RegistrationEpoch == request.auth.RegistrationEpoch &&
		s.qoderCatalogKey(current) == request.routeKey
}

func (s *Service) cancelStaleQoderProbes(authID string) {
	if s == nil {
		return
	}
	s.qoderProbeMu.Lock()
	for id, probes := range s.qoderAccountProbes {
		if authID != "" && id != authID {
			continue
		}
		for probe := range probes {
			if !s.qoderAccountProbeCurrent(probe) {
				probe.cancel()
				// An updated snapshot with the same token may start immediately
				// instead of joining an obsolete leader still unwinding its request.
				qoderCatalogGroup.Forget(probe.flightKey)
				delete(probes, probe)
			}
		}
		if len(probes) == 0 {
			delete(s.qoderAccountProbes, id)
		}
	}
	for id, active := range s.qoderPersistActive {
		if authID != "" && id != authID {
			continue
		}
		if active != nil && !s.qoderCatalogPersistCurrent(active) {
			if active.cancel != nil {
				active.cancel()
			}
		}
	}
	for id, pending := range s.qoderPersistPending {
		if authID != "" && id != authID {
			continue
		}
		if pending != nil && !s.qoderCatalogPersistCurrent(pending) {
			s.qoderPersistPending[id] = nil
		}
	}
	s.qoderProbeMu.Unlock()
	s.signalQoderModelRefresh()
}

func (s *Service) qoderModelRefreshWake() chan struct{} {
	s.qoderProbeMu.Lock()
	defer s.qoderProbeMu.Unlock()
	if s.qoderRefreshWake == nil {
		s.qoderRefreshWake = make(chan struct{}, 1)
	}
	return s.qoderRefreshWake
}

func (s *Service) signalQoderModelRefresh() {
	if s == nil {
		return
	}
	select {
	case s.qoderModelRefreshWake() <- struct{}{}:
	default:
	}
}
