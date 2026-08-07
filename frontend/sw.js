// Service Worker for Chintan PWA
const CACHE_NAME = 'chintan-v3';
const STATIC_CACHE = 'chintan-static-v3';
const API_CACHE = 'chintan-api-v3';

// Files to cache for offline use
const STATIC_FILES = [
    './',
    './index.html',
    './css/styles.css',
    './js/app.js',
    './js/api.js',
    './js/auth.js',
    './js/ui.js',
    './js/capture.js',
    './js/notes.js',
    './js/settings.js',
    './js/config.js',
    './manifest.json',
    './assets/icon.svg'
];

// API endpoints to cache (for offline reading)
const CACHEABLE_APIS = [
    '/v1/notes',
    '/v1/settings'
];

// Install event - cache static files
self.addEventListener('install', (event) => {
    console.log('Service Worker: Installing...');
    
    event.waitUntil(
        caches.open(STATIC_CACHE)
            .then(cache => {
                console.log('Service Worker: Caching static files');
                return cache.addAll(STATIC_FILES);
            })
            .then(() => {
                console.log('Service Worker: Static files cached');
                return self.skipWaiting();
            })
    );
});

// Activate event - clean up old caches
self.addEventListener('activate', (event) => {
    console.log('Service Worker: Activating...');
    
    event.waitUntil(
        caches.keys().then(cacheNames => {
            return Promise.all(
                cacheNames
                    .filter(cacheName => {
                        // Delete old caches
                        return cacheName !== STATIC_CACHE && 
                               cacheName !== API_CACHE &&
                               cacheName.startsWith('chintan-');
                    })
                    .map(cacheName => {
                        console.log('Service Worker: Deleting old cache:', cacheName);
                        return caches.delete(cacheName);
                    })
            );
        }).then(() => {
            console.log('Service Worker: Activated');
            return self.clients.claim();
        })
    );
});

// Fetch event - serve from cache when offline
self.addEventListener('fetch', (event) => {
    const request = event.request;
    const url = new URL(request.url);
    
    // Skip non-GET requests
    if (request.method !== 'GET') {
        return;
    }
    
    // Skip chrome-extension and other non-http(s) requests
    if (!url.protocol.startsWith('http')) {
        return;
    }

    event.respondWith(handleFetch(request));
});

async function handleFetch(request) {
    const url = new URL(request.url);
    
    try {
        // Handle static files (same origin)
        if (url.origin === self.location.origin) {
            return await handleStaticRequest(request);
        }
        
        // Handle API requests
        if (isApiRequest(url)) {
            return await handleApiRequest(request);
        }
        
        // Handle external resources (fonts, etc.)
        return await handleExternalRequest(request);
        
    } catch (error) {
        console.error('Service Worker: Fetch error:', error);
        return new Response('Offline', { 
            status: 503, 
            statusText: 'Service Unavailable' 
        });
    }
}

// Handle static files with cache-first strategy
async function handleStaticRequest(request) {
    const cache = await caches.open(STATIC_CACHE);
    const cachedResponse = await cache.match(request);
    
    if (cachedResponse) {
        // Return cached version
        return cachedResponse;
    }
    
    try {
        // Try to fetch from network
        const response = await fetch(request);
        
        // Cache successful responses
        if (response.status === 200) {
            cache.put(request, response.clone());
        }
        
        return response;
    } catch (error) {
        // If we can't fetch and don't have cache, try to return the main page
        if (request.mode === 'navigate') {
            const indexCache = await cache.match('./index.html');
            if (indexCache) {
                return indexCache;
            }
        }
        throw error;
    }
}

// Handle API requests with network-first strategy
async function handleApiRequest(request) {
    const cache = await caches.open(API_CACHE);
    
    try {
        // Try network first
        const response = await fetch(request);
        
        // Cache successful GET responses for certain endpoints
        if (response.status === 200 && shouldCacheApiResponse(request)) {
            cache.put(request, response.clone());
        }
        
        return response;
        
    } catch (error) {
        // If network fails, try cache for read-only requests
        if (request.method === 'GET') {
            const cachedResponse = await cache.match(request);
            if (cachedResponse) {
                // Add offline header
                const offlineResponse = new Response(cachedResponse.body, {
                    status: cachedResponse.status,
                    statusText: cachedResponse.statusText,
                    headers: {
                        ...Object.fromEntries(cachedResponse.headers.entries()),
                        'X-Served-By': 'ServiceWorker-Cache',
                        'X-Offline': 'true'
                    }
                });
                return offlineResponse;
            }
        }
        throw error;
    }
}

// Handle external requests (API + fonts). Never invent status codes —
// a failed cross-origin fetch must surface to the page, not become HTTP 404.
async function handleExternalRequest(request) {
    return fetch(request);
}

// Check if request is to our API (cross-origin execute-api host)
function isApiRequest(url) {
    return url.hostname.includes('execute-api') &&
           url.hostname.endsWith('.amazonaws.com') &&
           url.pathname.startsWith('/v1/');
}

// Check if API response should be cached
function shouldCacheApiResponse(request) {
    const url = new URL(request.url);
    
    // Only cache certain read-only endpoints
    return CACHEABLE_APIS.some(endpoint => 
        url.pathname.startsWith(endpoint)
    );
}

// Background sync for offline actions (future enhancement)
self.addEventListener('sync', (event) => {
    console.log('Service Worker: Background sync triggered:', event.tag);
    
    if (event.tag === 'background-sync-captures') {
        event.waitUntil(syncCapturesWhenOnline());
    }
});

// Placeholder for syncing captures when back online
async function syncCapturesWhenOnline() {
    // This would handle offline capture uploads when connection is restored
    console.log('Service Worker: Syncing offline captures...');
    
    // Implementation would:
    // 1. Get offline captures from IndexedDB
    // 2. Upload them when online
    // 3. Notify main app of success/failure
}

// Handle push notifications (future enhancement)
self.addEventListener('push', (event) => {
    console.log('Service Worker: Push notification received');
    
    if (event.data) {
        const data = event.data.json();
        
        event.waitUntil(
            self.registration.showNotification(data.title, {
                body: data.body,
                icon: './assets/icon.svg',
                badge: './assets/icon.svg',
                tag: data.tag || 'chintan-notification',
                data: data.data || {}
            })
        );
    }
});

// Handle notification clicks
self.addEventListener('notificationclick', (event) => {
    console.log('Service Worker: Notification clicked');
    
    event.notification.close();
    
    event.waitUntil(
        self.clients.openWindow('./')
    );
});

console.log('Service Worker: Script loaded');