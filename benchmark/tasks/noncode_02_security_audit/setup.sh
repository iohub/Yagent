#!/bin/bash
set -e
REPO="/tmp/repos/noncode_02_security_audit"
rm -rf "$REPO"
mkdir -p "$REPO/app"
cd "$REPO"

cat > app/config.py << 'PYEOF'
# Security issue: hardcoded secrets
DATABASE_URL = "postgresql://admin:SuperSecret123@localhost:5432/mydb"
SECRET_KEY = "mysecret123"
JWT_SECRET = "mysecret123"
API_KEY = "sk-1234567890abcdef"
DEBUG = True
CORS_ORIGINS = ["*"]
PYEOF

cat > app/database.py << 'PYEOF'
import sqlite3
from app.config import DATABASE_URL

def get_connection():
    return sqlite3.connect("app.db")

# Security issue: SQL injection via string formatting
def get_user(username: str):
    conn = get_connection()
    query = f"SELECT * FROM users WHERE username = '{username}'"
    return conn.execute(query).fetchone()

def get_user_by_email(email: str):
    conn = get_connection()
    query = "SELECT * FROM users WHERE email = '%s'" % email
    return conn.execute(query).fetchone()

def create_user(username: str, password: str, email: str):
    conn = get_connection()
    query = f"INSERT INTO users (username, password, email) VALUES ('{username}', '{password}', '{email}')"
    conn.execute(query)
    conn.commit()
PYEOF

cat > app/auth.py << 'PYEOF'
import jwt
from app.config import JWT_SECRET

# Security issue: hardcoded secret, no algorithm verification
def create_token(user_id: int) -> str:
    payload = {"user_id": user_id, "role": "user"}
    return jwt.encode(payload, JWT_SECRET, algorithm="HS256")

def decode_token(token: str) -> dict:
    # Security issue: no signature verification (verify=False equivalent)
    return jwt.decode(token, options={"verify_signature": False})

def verify_token(token: str) -> dict:
    try:
        # Security issue: hardcoded secret
        return jwt.decode(token, JWT_SECRET, algorithms=["HS256"])
    except Exception:
        return None
PYEOF

cat > app/user.py << 'PYEOF'
from app.database import create_user, get_user

# Security issue: password stored in plaintext
def register_user(username: str, password: str, email: str):
    # No password hashing
    create_user(username, password, email)

def login_user(username: str, password: str) -> dict:
    user = get_user(username)
    if user is None:
        return None
    # Security issue: plaintext password comparison
    stored_password = user[2]  # password column
    if password == stored_password:
        from app.auth import create_token
        token = create_token(user[0])
        return {"token": token, "user_id": user[0]}
    return None

def get_user_profile(user_id: int):
    # Security issue: No authorization check - any user can view any profile
    return {"user_id": user_id, "profile": "sensitive data"}
PYEOF

cat > app/main.py << 'PYEOF'
from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from app.config import CORS_ORIGINS
from app.user import register_user, login_user

app = FastAPI()

# Security issue: CORS allows all origins
app.add_middleware(
    CORSMiddleware,
    allow_origins=CORS_ORIGINS,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

@app.post("/register")
async def register(request: Request):
    data = await request.json()
    # Security issue: no input validation
    register_user(data["username"], data["password"], data.get("email", ""))
    return {"status": "ok"}

@app.post("/login")
async def login(request: Request):
    data = await request.json()
    result = login_user(data["username"], data["password"])
    if result:
        return result
    return {"error": "invalid credentials"}, 401

@app.get("/user/{user_id}")
async def get_user(user_id: int):
    from app.user import get_user_profile
    return get_user_profile(user_id)
PYEOF

cat > requirements.txt << 'EOF'
fastapi==0.115.0
uvicorn==0.30.0
pyjwt==2.8.0
EOF

cat > README.md << 'EOF'
# User API Service

A FastAPI-based user management service.

## Running
```bash
pip install -r requirements.txt
uvicorn app.main:app --reload
```
## API Endpoints
- POST /register - Register a new user
- POST /login - Login
- GET /user/{id} - Get user profile
EOF

echo "noncode_02_security_audit setup done"
