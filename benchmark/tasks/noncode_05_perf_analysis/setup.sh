#!/bin/bash
set -e
REPO="/tmp/repos/noncode_05_perf_analysis"
rm -rf "$REPO"
mkdir -p "$REPO"/{app,data}
cd "$REPO"

cat > app/api.py << 'EOF'
# Performance issue: N+1 queries, no caching
import time
from app.data_loader import load_users, load_orders

_user_cache = None  # Should be used but isn't

def get_all_users():
    return load_users()

def get_user_orders(user_id: int):
    # N+1 problem: loads all orders then filters in Python
    all_orders = load_orders()
    return [o for o in all_orders if o["user_id"] == user_id]

def get_users_with_order_counts():
    # Performance issue: N+1 queries
    users = load_users()
    result = []
    for user in users:
        orders = get_user_orders(user["id"])
        result.append({
            "username": user["username"],
            "order_count": len(orders),
        })
    return result

def get_user_profile(user_id: int):
    # Performance issue: loads all data for single user lookup
    users = load_users()
    for u in users:
        if u["id"] == user_id:
            orders = get_user_orders(user_id)
            return {"user": u, "recent_orders": orders[-5:]}
    return None

def get_top_user():
    # Performance issue: recomputes from scratch every call (no caching)
    users_with_counts = get_users_with_order_counts()
    users_with_counts.sort(key=lambda x: x["order_count"], reverse=True)
    return users_with_counts[:3]
EOF

cat > app/processor.py << 'EOF'
# Performance issues: O(n^2), redundant computation, repeated regex compilation
import re

def deduplicate(items: list) -> list:
    # O(n^2): nested loops instead of using set/dict
    result = []
    for item in items:
        found = False
        for existing in result:
            if existing == item:
                found = True
                break
        if not found:
            result.append(item)
    return result

def filter_valid_emails(emails: list[str]) -> list[str]:
    # Repeated regex compilation inside loop
    results = []
    for email in emails:
        pattern = re.compile(r'^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$')
        if pattern.match(email):
            results.append(email)
    return results

def find_related_pairs(items: list) -> list:
    # O(n^2) algorithm
    pairs = []
    for i in range(len(items)):
        for j in range(i+1, len(items)):
            if items[i] % items[j] == 0 or items[j] % items[i] == 0:
                pairs.append((items[i], items[j]))
    return pairs

def extract_keywords(text: str) -> list[str]:
    # Redundant computation: splits and joins repeatedly
    words = text.lower().split()
    words = deduplicate(words)
    words = [w.strip('.,!?;:"()[]{}') for w in words]
    words = [w for w in words if len(w) > 3]
    words = deduplicate(words)  # redundant second dedup
    return words

def compute_word_frequency(texts: list[str]) -> dict:
    # O(n*m) where n=texts, m=words per text
    freq = {}
    for text in texts:
        for word in text.lower().split():
            word = word.strip('.,!?')
            if word not in freq:
                freq[word] = 0
            freq[word] += 1
    # Redundant sort just to get top items
    sorted_items = sorted(freq.items(), key=lambda x: x[1], reverse=True)
    return dict(sorted_items[:20])
EOF

cat > app/data_loader.py << 'EOF'
# Performance issues: loads entire file into memory, no streaming
import csv
import json

def load_users():
    # Loads entire CSV into memory at once
    with open("data/users.csv") as f:
        reader = csv.DictReader(f)
        return list(reader)  # reads everything into memory

def load_orders():
    with open("data/orders.csv") as f:
        return f.readlines()  # raw lines, parsed each time
EOF

cat > app/cache.py << 'EOF'
# Performance issues: unbounded dict, no TTL, no eviction

_cache = {}

def cache_get(key: str):
    return _cache.get(key)

def cache_set(key: str, value):
    # Never expires, never evicts — potential memory leak
    _cache[key] = value

def cache_clear():
    _cache.clear()

def get_user_from_cache(user_id: int):
    cached = cache_get(f"user:{user_id}")
    if cached is not None:
        return cached
    # Load and cache (but cache never expires)
    from app.data_loader import load_users
    users = load_users()
    for u in users:
        cache_set(f"user:{u['id']}", u)
    return cache_get(f"user:{user_id}")
EOF

mkdir -p data
cat > data/users.csv << 'EOF'
id,username,email
1,alice,alice@example.com
2,bob,bob@example.com
3,carol,carol@example.com
4,dave,dave@example.com
5,eve,eve@example.com
6,frank,frank@example.com
7,grace,grace@example.com
8,henry,henry@example.com
9,iris,iris@example.com
10,jack,jack@example.com
11,kate,kate@example.com
12,leo,leo@example.com
13,maria,maria@example.com
14,nick,nick@example.com
15,olivia,olivia@example.com
16,paul,paul@example.com
17,quinn,quinn@example.com
18,rose,rose@example.com
19,sam,sam@example.com
20,tina,tina@example.com
21,uma,uma@example.com
22,vic,vic@example.com
23,wendy,wendy@example.com
24,xander,xander@example.com
25,yara,yara@example.com
26,zack,zack@example.com
27,anna,anna@example.com
28,ben,ben@example.com
29,clara,clara@example.com
30,dan,dan@example.com
31,ella,ella@example.com
32,finn,finn@example.com
33,gina,gina@example.com
34,hugo,hugo@example.com
35,ivy,ivy@example.com
36,jade,jade@example.com
37,kai,kai@example.com
38,luna,luna@example.com
39,max,max@example.com
40,nora,nora@example.com
41,owen,owen@example.com
42,pia,pia@example.com
43,quinn2,quinn2@example.com
44,ray,ray@example.com
45,sue,sue@example.com
46,tom,tom@example.com
47,ursula,ursula@example.com
48,vince,vince@example.com
49,willa,willa@example.com
50,xen,xen@example.com
EOF

cat > data/orders.csv << 'EOF'
user_id,amount,status
1,99.99,completed
1,149.50,pending
2,200.00,completed
2,50.00,cancelled
3,75.25,completed
3,120.00,pending
3,30.00,completed
4,500.00,completed
5,15.99,pending
6,89.00,completed
7,250.00,pending
7,100.00,completed
8,45.50,completed
9,300.00,completed
9,80.00,cancelled
10,1200.00,completed
EOF

cat > README.md << 'EOF'
# User Analytics Service

A Python service for analyzing user data and orders.

## Structure
- `app/api.py` - Public API endpoints
- `app/processor.py` - Data processing utilities
- `app/data_loader.py` - File I/O and data loading
- `app/cache.py` - Simple in-memory cache
- `data/` - CSV data files

## Usage
```python
from app.api import get_users_with_order_counts, get_top_user
print(get_top_user())
```
EOF

echo "noncode_05_perf_analysis setup done"
