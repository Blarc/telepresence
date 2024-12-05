package dns

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	maxRecursionTestRetries = 10
	recursionTestTimeout    = 500 * time.Millisecond
)

// Worker places a file under the /etc/resolver directory so that it is picked up by the
// macOS resolver. The file is configured with a single nameserver that points to the local IP
// that the Telepresence DNS server listens to. The file is removed, and the DNS is flushed when
// the worker terminates
//
// For more information about /etc/resolver files, please view the man pages available at
//
//	man 5 resolver
//
// or, if not on a Mac, follow this link: https://www.manpagez.com/man/5/resolver/
func (s *Server) Worker(c context.Context, dev vif.Device, configureDNS func(net.IP, *net.UDPAddr)) error {
	resolverDirName := filepath.Join("/etc", "resolver")
	dlog.Infof(c, "Starting DNS server setup in %s", resolverDirName)

	listener, err := newLocalUDPListener(c)
	if err != nil {
		dlog.Errorf(c, "Failed to create UDP listener: %v", err)
		return err
	}
	dnsAddr, err := splitToUDPAddr(listener.LocalAddr())
	if err != nil {
		dlog.Errorf(c, "Failed to get UDP address from listener: %v", err)
		return err
	}
	dlog.Infof(c, "Created DNS listener on %v", dnsAddr)
	configureDNS(nil, dnsAddr)

	err = os.MkdirAll(resolverDirName, 0o755)
	if err != nil {
		dlog.Errorf(c, "Failed to create resolver directory %s: %v", resolverDirName, err)
		return err
	}

	// Ensure lingering all telepresence.* files are removed.
	if err := s.removeResolverFiles(c, resolverDirName); err != nil {
		dlog.Errorf(c, "Failed to remove existing resolver files: %v", err)
		return err
	}

	defer func() {
		dlog.Info(c, "Cleaning up DNS server")
		if err := s.removeResolverFiles(c, resolverDirName); err != nil {
			dlog.Errorf(c, "Failed to remove resolver files during cleanup: %v", err)
		}
		s.flushDNS()
	}()

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		dlog.Info(c, "Starting DNS server goroutine")
		if err := s.updateResolverFiles(c, resolverDirName, dnsAddr); err != nil {
			dlog.Errorf(c, "Initial resolver file update failed: %v", err)
			return err
		}
		s.processSearchPaths(g, func(c context.Context, _ vif.Device) error {
			dlog.Debug(c, "Updating resolver files after search path change")
			return s.updateResolverFiles(c, resolverDirName, dnsAddr)
		}, dev)
		// Server will close the listener, so no need to close it here.
		dlog.Info(c, "Starting DNS server main loop")
		return s.Run(c, make(chan struct{}), []net.PacketConn{listener}, nil, s.resolveInCluster)
	})
	return g.Wait()
}

// removeResolverFiles performs rm -f /etc/resolver/telepresence.*.
func (s *Server) removeResolverFiles(c context.Context, resolverDirName string) error {
	files, err := os.ReadDir(resolverDirName)
	if err != nil {
		dlog.Errorf(c, "Failed to read resolver directory %s: %v", resolverDirName, err)
		return err
	}
	for _, file := range files {
		if n := file.Name(); strings.HasPrefix(n, "telepresence.") {
			fn := filepath.Join(resolverDirName, n)
			dlog.Debugf(c, "Removing resolver file %q", fn)
			if err := os.Remove(fn); err != nil {
				dlog.Errorf(c, "Failed to remove resolver file %s: %v", fn, err)
				return err
			}
		}
	}
	return nil
}

func (s *Server) updateResolverFiles(c context.Context, resolverDirName string, dnsAddr *net.UDPAddr) error {
	s.Lock()
	defer s.Unlock()

	dlog.Infof(c, "Updating resolver files in %s for DNS server at %v", resolverDirName, dnsAddr)
	nameservers := []string{dnsAddr.IP.String()}
	port := dnsAddr.Port

	// Create resolver file for each domain
	newDomainResolveFile := func(domain string) *dnsproxy.ResolveFile {
		dlog.Debugf(c, "Creating resolver file for domain %q", domain)
		return &dnsproxy.ResolveFile{
			Port:        port,
			Domain:      domain,
			Nameservers: nameservers,
		}
	}

	// All routes and include suffixes become domains
	dlog.Debug(c, "Processing routes and include suffixes for domain resolution")
	domains := make(map[string]*dnsproxy.ResolveFile, len(s.routes)+len(s.includeSuffixes))
	for route := range s.routes {
		dlog.Debugf(c, "Adding route domain: %s", route)
		domains[route] = newDomainResolveFile(route)
	}
	for _, sfx := range s.includeSuffixes {
		sfx = strings.TrimPrefix(sfx, ".")
		dlog.Debugf(c, "Adding include suffix domain: %s", sfx)
		domains[sfx] = newDomainResolveFile(sfx)
	}

	clusterDomain := strings.TrimSuffix(s.clusterDomain, ".")
	dlog.Debugf(c, "Adding cluster domain: %s", clusterDomain)
	domains[clusterDomain] = newDomainResolveFile(clusterDomain)
	domains[tel2SubDomain] = newDomainResolveFile(tel2SubDomain)

nextSearch:
	for _, search := range s.search {
		search = strings.TrimSuffix(search, ".")
		if df, ok := domains[search]; ok {
			dlog.Debugf(c, "Adding search domain to existing domain: %s", search)
			df.Search = append(df.Search, search)
			continue
		}
		for domain, df := range domains {
			if strings.HasSuffix(search, "."+domain) {
				dlog.Debugf(c, "Adding search domain %s to parent domain %s", search, domain)
				df.Search = append(df.Search, search)
				continue nextSearch
			}
		}
	}

	// Remove old resolver files
	for domain := range s.domains {
		if _, ok := domains[domain]; !ok {
			nsFile := domainResolverFile(resolverDirName, domain)
			dlog.Infof(c, "Removing obsolete resolver file: %s", nsFile)
			if err := os.Remove(nsFile); err != nil {
				dlog.Error(c, err)
			}
			delete(s.domains, domain)
		}
	}

	// Update resolver files
	for domain, rf := range domains {
		nsFile := domainResolverFile(resolverDirName, domain)
		if _, ok := s.domains[domain]; ok {
			if oldRf, err := dnsproxy.ReadResolveFile(nsFile); err != nil && rf.Equals(oldRf) {
				dlog.Debugf(c, "Skipping unchanged resolver file: %s", nsFile)
				continue
			}
			dlog.Infof(c, "Regenerating resolver file: %s", nsFile)
		} else {
			s.domains[domain] = struct{}{}
			dlog.Infof(c, "Generating new resolver file: %s", nsFile)
		}
		if err := rf.Write(nsFile); err != nil {
			dlog.Errorf(c, "Failed to write resolver file %s: %v", nsFile, err)
			return err
		}
	}

	dlog.Info(c, "Successfully updated all resolver files")
	s.flushDNS()
	return nil
}

func domainResolverFile(resolverDirName, domain string) string {
	return filepath.Join(resolverDirName, "telepresence."+domain)
}
