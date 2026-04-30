#!/bin/bash
set -e
REPO="/tmp/repos/rust_01_lru_cache"
rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"
cargo init --lib --name lru_cache 2>/dev/null

# Add test file
mkdir -p tests
cat > tests/lru_test.rs << 'TESTEOF'
use lru_cache::LruCache;
use std::sync::Arc;
use std::thread;
use std::time::Duration;

#[test]
fn test_new_and_basic_ops() {
    let mut cache = LruCache::new(2);
    assert_eq!(cache.len(), 0);
    cache.put("a".to_string(), 1);
    assert_eq!(cache.len(), 1);
    assert_eq!(cache.get(&"a".to_string()), Some(&1));
}

#[test]
fn test_capacity_eviction() {
    let mut cache = LruCache::new(2);
    cache.put("a".to_string(), 1);
    cache.put("b".to_string(), 2);
    cache.put("c".to_string(), 3);
    assert_eq!(cache.len(), 2);
    assert_eq!(cache.get(&"a".to_string()), None); // a evicted
    assert_eq!(cache.get(&"b".to_string()), Some(&2));
    assert_eq!(cache.get(&"c".to_string()), Some(&3));
}

#[test]
fn test_lru_order() {
    let mut cache = LruCache::new(2);
    cache.put("a".to_string(), 1);
    cache.put("b".to_string(), 2);
    cache.get(&"a".to_string()); // access a, making b LRU
    cache.put("c".to_string(), 3);
    assert_eq!(cache.get(&"a".to_string()), Some(&1)); // a still here
    assert_eq!(cache.get(&"b".to_string()), None); // b evicted
    assert_eq!(cache.get(&"c".to_string()), Some(&3));
}

#[test]
fn test_update_existing() {
    let mut cache = LruCache::new(2);
    cache.put("a".to_string(), 1);
    cache.put("a".to_string(), 100);
    assert_eq!(cache.get(&"a".to_string()), Some(&100));
    assert_eq!(cache.len(), 1);
}

#[test]
fn test_remove() {
    let mut cache = LruCache::new(3);
    cache.put("a".to_string(), 1);
    cache.put("b".to_string(), 2);
    cache.remove(&"a".to_string());
    assert_eq!(cache.get(&"a".to_string()), None);
    assert_eq!(cache.len(), 1);
}

#[test]
fn test_clear() {
    let mut cache = LruCache::new(3);
    cache.put("a".to_string(), 1);
    cache.put("b".to_string(), 2);
    cache.clear();
    assert_eq!(cache.len(), 0);
    assert_eq!(cache.get(&"a".to_string()), None);
}

#[test]
fn test_contains() {
    let mut cache = LruCache::new(3);
    cache.put("a".to_string(), 1);
    assert!(cache.contains(&"a".to_string()));
    assert!(!cache.contains(&"b".to_string()));
}

#[test]
fn test_thread_safety() {
    let cache = Arc::new(LruCache::new(100));
    let mut handles = vec![];
    for i in 0..10 {
        let c = Arc::clone(&cache);
        handles.push(thread::spawn(move || {
            for j in 0..100 {
                c.lock().unwrap().put(format!("key-{}-{}", i, j), i * 1000 + j);
            }
        }));
    }
    for h in handles {
        h.join().unwrap();
    }
    let locked = cache.lock().unwrap();
    assert!(locked.len() > 0);
    assert!(locked.len() <= 100);
}

#[test]
fn test_ttl_expiry() {
    let mut cache = LruCache::new(5);
    cache.put_with_ttl("a".to_string(), 1, Duration::from_millis(50));
    assert_eq!(cache.get(&"a".to_string()), Some(&1));
    thread::sleep(Duration::from_millis(100));
    assert_eq!(cache.get(&"a".to_string()), None); // expired
}
TESTEOF

# Update lib.rs to export the module
cat > src/lib.rs << 'LIBEOF'
pub struct LruCache<K, V> { /* implement */ }

impl<K: std::cmp::Eq + std::hash::Hash + Clone, V: Clone> LruCache<K, V> {
    pub fn new(capacity: usize) -> Self { todo!() }
    pub fn get(&mut self, key: &K) -> Option<&V> { todo!() }
    pub fn put(&mut self, key: K, value: V) { todo!() }
    pub fn put_with_ttl(&mut self, key: K, value: V, ttl: std::time::Duration) { todo!() }
    pub fn remove(&mut self, key: &K) { todo!() }
    pub fn clear(&mut self) { todo!() }
    pub fn len(&self) -> usize { todo!() }
    pub fn contains(&self, key: &K) -> bool { todo!() }
    pub fn lock(&self) -> Result<std::sync::MutexGuard<Self>, std::sync::PoisonError<std::sync::MutexGuard<Self>>> { todo!() }
}
LIBEOF

echo "rust_01_lru_cache setup done"
