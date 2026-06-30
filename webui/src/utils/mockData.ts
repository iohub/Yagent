import { Message } from '../types';

export interface MockRawMessage {
  type: string;
  data: any;
  timestamp: number;
}

const mockMessages: any[] = [
  {
    "type": "status_update",
    "timestamp": 1768294885048,
    "data": "DirectorAgent is thinking (step 1/30)..."
  },
  {
    "type": "tool_call_start",
    "timestamp": 1768294897442,
    "tool_name": "delegate_repo",
    "data": {
      "arguments": "{\"task\": \"查找项目中名为 crud.py 的文件，并分析其所在的目录结构和文件内容，确认其主要功能和现有的注释语言。\"}",
      "tool_name": "delegate_repo"
    }
  },
  {
    "type": "ai_response",
    "timestamp": 1768294897444,
    "data": "## Thought Process\n---\n1.  **State Analysis**:\n    *   **Current Goal**: 将 `crud.py` 文件中的注释从英文（或其他语言）修改为中文。\n    *   **Current Step Status**: 任务刚开始，尚未获取任何关于项目结构或文件内容的信息。\n    *   **Reasoning**: 根据工作流策略，我必须首先使用 `Repo-Agent` 来分析仓库结构，以确认 `crud.py` 的存在及其位置。这是获取“心智地图”的强制性步骤。\n---\n2.  **Plan Update**:\n    *   [x] 1. 分析仓库结构，定位 `crud.py` 文件。\n    *   [ ] 2. 读取 `crud.py` 的内容，分析现有的注释风格和内容。\n    *   [ ] 3. 将注释翻译/修改为中文。\n    *   [ ] 4. 验证修改后的代码语法是否正确。\n\n我将首先调用 `delegate_repo` 来查找文件。"
  },
  {
    "type": "memory_change",
    "timestamp": 1768294897444
  },
  {
    "type": "status_update",
    "timestamp": 1768294897456,
    "data": "RepoAgent is thinking (step 1/30)..."
  },
  {
    "type": "tool_call_start",
    "timestamp": 1768294902662,
    "tool_name": "print_dir_tree",
    "data": {
      "arguments": "{\"dir_path\": \"/Users/ydl/projects/demo\", \"max_depth\": 3}",
      "tool_name": "print_dir_tree"
    }
  },
  {
    "type": "ai_response",
    "timestamp": 1768294902663,
    "data": "### 思考过程\n\n首先，我需要分析提供的仓库信息，包括目录树、核心函数和文件骨架。目录树显示这是一个包含前端和后端代码的项目，后端使用Python（FastAPI框架），前端可能使用JavaScript/TypeScript（因为有dist/assets目录）。核心函数列表主要来自dist/assets中的JavaScript文件，表明有编译后的前端代码。文件骨架部分为空，所以我需要使用工具来获取更多细节。\n\n我将使用高优先级工具来探索：\n1. 使用`print_dir_tree`获取更详细的目录结构，特别是demo目录下的内容。\n2. 使用`query_code_skeleton`或`query_code_snippet`来查看关键Python文件（如main.py, models.py）的结构，以确认技术栈和核心组件。\n3. 如果需要，使用`semantic_search`来查找特定模式，如路由或数据库连接。\n\n由于核心函数是JavaScript的，我可能需要检查前端部分，但用户查询的重点是代码分析，所以我将专注于后端Python部分，因为目录树中的demo/app是主要的源代码。\n\n最终，我将总结技术栈、结构、核心组件和入口点。\n\n### 思考工具使用\n\n我将首先使用`print_dir_tree`来获取demo目录的完整结构，以确认所有文件和目录。"
  },
  {
    "type": "status_update",
    "timestamp": 1768294902665,
    "data": "RepoAgent is thinking (step 2/30)..."
  },
  {
    "type": "tool_call_result",
    "timestamp": 1768294902666,
    "tool_name": "print_dir_tree",
    "data": {
      "result": "\"├── .gitignore\\n├── app\\n│   ├── auth.py\\n│   ├── crud.py\\n│   ├── database.py\\n│   ├── main.py\\n│   ├── models.py\\n│   ├── routers\\n│   │   ├── auth.py\\n│   │   ├── expenses.py\\n│   │   └── tasks.py\\n│   └── schemas.py\\n└── requirements.txt\\n\"",
      "tool_name": "print_dir_tree"
    }
  },
  {
    "type": "tool_call_start",
    "timestamp": 1768294931571,
    "tool_name": "query_code_skeleton",
    "data": {
      "arguments": "{\"filepaths\": [\"/Users/ydl/projects/demo/app/main.py\", \"/Users/ydl/projects/demo/app/models.py\", \"/Users/ydl/projects/demo/app/database.py\"]}",
      "tool_name": "query_code_skeleton"
    }
  },
  {
    "type": "ai_response",
    "timestamp": 1768294931573,
    "data": "### 思考过程\n\n现在我有了更清晰的目录结构。demo目录下有app子目录，包含Python文件，以及requirements.txt。这表明这是一个Python后端项目，可能使用FastAPI（因为有routers目录和main.py）。核心函数列表中的JavaScript文件可能来自前端，但目录树中没有明显的前端源代码，只有dist/assets，这可能是构建后的输出。我将专注于后端分析。\n\n接下来，我需要查看关键文件的内容来确认技术栈和核心组件。我将使用`query_code_skeleton`来获取main.py、models.py和database.py的骨架，以了解结构和依赖。\n\n### 思考工具使用\n\n使用`query_code_skeleton`来查看app目录下的关键文件。"
  },
  {
    "type": "status_update",
    "timestamp": 1768294931590,
    "data": "RepoAgent is thinking (step 3/30)..."
  },
  {
    "type": "tool_call_result",
    "timestamp": 1768294931591,
    "tool_name": "query_code_skeleton",
    "data": {
      "result": "{\"success\":true,\"data\":{\"skeletons\":[{\"filepath\":\"/Users/ydl/projects/demo/app/main.py\",\"language\":\"python\",\"skeleton_text\":\"@app.on_event(\\\"startup\\\")\\ndef on_startup()\\n\\n\\n@app.get(\\\"/\\\")\\ndef read_root()\\n  ...\"},{\"filepath\":\"/Users/ydl/projects/demo/app/models.py\",\"language\":\"python\",\"skeleton_text\":\"class User(Base)\\n  def __repr__(self)\\n    ...\\n      __tablename__ = \\\"users\\\"\\n      id = Column(Integer, primary_key=True, index=True)\\n      username = Column(String, unique=True, index=True, nullable=False)\\n      hashed_password = Column(String, nullable=False)\\n      expenses = relationship(\\\"Expense\\\", back_populates=\\\"user\\\", cascade=\\\"all, delete-orphan\\\")\\n\\n\\nclass Expense(Base)\\n  def __repr__(self)\\n    ...\\n      __tablename__ = \\\"expenses\\\"\\n      id = Column(Integer, primary_key=True, index=True)\\n      amount = Column(Float, nullable=False)\\n      category = Column(String, nullable=False)\\n      date = Column(String, nullable=False)  # Storing as string (ISO format)\\n      user_id = Column(Integer, ForeignKey(\\\"users.id\\\"), nullable=False)\\n      user = relationship(\\\"User\\\", back_populates=\\\"expenses\\\")\\n\\n\\nclass Task(Base)\\n  def __repr__(self)\\n    ...\\n      __tablename__ = \\\"tasks\\\"\\n      id = Column(Integer, primary_key=True, index=True)\\n      title = Column(String, nullable=False)\\n      description = Column(String, nullable=True)\\n      is_completed = Column(Boolean, default=False, nullable=False)\\n\\n\\ndef __repr__(self)\\n\\n\\ndef __repr__(self)\\n\\n\\ndef __repr__(self)\\n\"},{\"filepath\":\"/Users/ydl/projects/demo/app/database.py\",\"language\":\"python\",\"skeleton_text\":\"def get_db()\\n\"}]}}",
      "tool_name": "query_code_skeleton"
    }
  },
  {
    "type": "tool_call_start",
    "timestamp": 1768294935964,
    "tool_name": "query_code_skeleton",
    "data": {
      "arguments": "{\"filepaths\": [\"/Users/ydl/projects/demo/app/routers/auth.py\", \"/Users/ydl/projects/demo/app/routers/expenses.py\", \"/Users/ydl/projects/demo/app/routers/tasks.py\"]}",
      "tool_name": "query_code_skeleton"
    }
  },
  {
    "type": "ai_response",
    "timestamp": 1768294935965,
    "data": "### 思考过程\n\n从骨架中，我可以看到：\n- main.py 有事件处理和根路由。\n- models.py 定义了User、Expense和Task模型，使用SQLAlchemy（从Column和relationship推断）。\n- database.py 有一个get_db函数，可能用于数据库会话管理。\n\n这确认了后端使用SQLAlchemy进行ORM。现在，我需要查看routers目录下的文件来了解API路由。我将使用`query_code_skeleton`来查看routers/auth.py、expenses.py和tasks.py。\n\n### 思考工具使用\n\n使用`query_code_skeleton`来查看routers目录下的文件。"
  },
  {
    "type": "status_update",
    "timestamp": 1768294935974,
    "data": "RepoAgent is thinking (step 4/30)..."
  },
  {
    "type": "tool_call_result",
    "timestamp": 1768294935974,
    "tool_name": "query_code_skeleton",
    "data": {
      "result": "{\"success\":true,\"data\":{\"skeletons\":[{\"filepath\":\"/Users/ydl/projects/demo/app/routers/auth.py\",\"language\":\"python\",\"skeleton_text\":\"@router.post(\\\"/register\\\", response_model=schemas.User)\\ndef register(user: schemas.UserCreate, db: Session = Depends(get_db))\\n\\n\\n@router.post(\\\"/login\\\", response_model=schemas.Token)\\ndef login(form_data: OAuth2PasswordRequestForm = Depends(), db: Session = Depends(get_db))\\n\"},{\"filepath\":\"/Users/ydl/projects/demo/app/routers/expenses.py\",\"language\":\"python\",\"skeleton_text\":\"@router.post(\\\"/\\\", response_model=schemas.Expense)\\ndef create_expense(\\n    expense: schemas.ExpenseCreate,\\n    current_user: schemas.User = Depends(auth.get_current_user),\\n    db: Session = Depends(get_db)\\n)\\n\\n\\n@router.get(\\\"/\\\", response_model=List[schemas.Expense])\\ndef read_expenses(\\n    skip: int = 0,\\n    limit: int = 100,\\n    current_user: schemas.User = Depends(auth.get_current_user),\\n    db: Session = Depends(get_db)\\n)\\n\\n\\n@router.get(\\\"/summary\\\")\\ndef get_expense_summary(\\n    current_user: schemas.User = Depends(auth.get_current_user),\\n    db: Session = Depends(get_db)\\n)\\n\"},{\"filepath\":\"/Users/ydl/projects/demo/app/routers/tasks.py\",\"language\":\"python\",\"skeleton_text\":\"class TaskBase(BaseModel)\\n      title: str\\n      description: str | None = None\\n\\n\\nclass TaskCreate(TaskBase)\\n  ...\\n\\nclass TaskUpdate(BaseModel)\\n      title: str | None = None\\n      description: str | None = None\\n      is_completed: bool | None = None\\n\\n\\nclass TaskResponse(TaskBase)\\n      id: int\\n      is_completed: bool\\n\\n\\n@router.post(\\\"/\\\", response_model=TaskResponse)\\ndef create_task(task: TaskCreate, db: Session = Depends(get_db))\\n\\n\\n@router.get(\\\"/\\\", response_model=List[TaskResponse])\\ndef read_tasks(skip: int = 0, limit: int = 100, db: Session = Depends(get_db))\\n\\n\\n@router.get(\\\"/{task_id}\\\", re[... 417 chars omitted ...]...\"}]}}",
      "tool_name": "query_code_skeleton"
    }
  },
  {
    "type": "tool_call_start",
    "timestamp": 1768295009407,
    "tool_name": "read_file",
    "data": {
      "arguments": "{\"target_file\": \"/Users/ydl/projects/demo/app/crud.py\", \"should_read_entire_file\": true, \"start_line_one_indexed\": 1, \"end_line_one_indexed_inclusive\": 100}",
      "tool_name": "read_file"
    }
  },
  {
    "type": "tool_call_result",
    "timestamp": 1768295009414,
    "tool_name": "read_file",
    "data": {
      "result": "{\"content\":\"# CRUD operations for User and Expense models using SQLAlchemy\\nfrom sqlalchemy.orm import Session\\nfrom sqlalchemy import func\\nfrom app.models import User, Expense\\nfrom app.schemas import UserCreate, ExpenseCreate\\n\\n\\ndef get_user_by_username(db: Session, username: str):\\n    \\\"\\\"\\\"\\n    Retrieve a user by username using SQLAlchemy query.\\n    Returns User object or None.\\n    \\\"\\\"\\\"\\n    return db.query(User).filter(User.username == username).first()\\n\\n\\ndef create_user(db: Session, user: UserCreate, hashed_password: str):\\n    \\\"\\\"\\\"\\n    Create a new user with hashed password.\\n    - Check if username exists.\\n    - Create User model instance with hashed password.\\n    - Add to session, commit, refresh, and return.\\n    Returns created User or None if username exists.\\n    \\\"\\\"\\\"\\n    # Check if username already exists\\n    existing_user = get_user_by_username(db, user.username)\\n    if existing_user:\\n        return None\\n    \\n    # Create new user instance\\n    db_user = User(\\n        username=user.username,\\n        hashed_password=hashed_password\\n    )\\n    db.add(db_user)\\n    db.commit()\\n    db.refresh(db_user)\\n    return db_user\\n\\n\\ndef create_expense(db: Session, expense: ExpenseCreate, user_id: int):\\n    \\\"\\\"\\\"\\n    Create a new expense for a specific user.\\n    - Create Expense model instance.\\n    - Add to session, commit, refresh, and return.\\n    Returns created Expense.\\n    \\\"\\\"\\\"\\n    db_expense = Expense(\\n        amount=expense.amount,\\n        category=expense.category,\\n        date=expense.date,\\n        user_id=user_id\\n    )\\n    db.add(db_expense)\\n    db.commit()\\n    db.refresh(db_expense)\\n    return db_expense\\n\\n\\ndef get_expenses_by_user(db: Session, user_id: int, skip: int = 0, limit: int = 100):\\n    \\\"\\\"\\\"\\n    Retrieve all expenses for a specific user with pagination.\\n    Returns list of Expense objects.\\n    \\\"\\\"\\\"\\n    return db.query(Expense).filter(Expense.user_id == user_id).offset(skip).limit(limit).all()\\n\\n\\ndef get_expense_summary(db: Session, user_id: int):\\n    \\\"\\\"\\\"\\n    Calculate total expenses for a specific user.\\n    Returns total amount as float.\\n    \\\"\\\"\\\"\\n    result = db.query(func.sum(Expense.amount)).filter(Expense.user_id == user_id).scalar()\\n    return result or 0.0\",\"lines\":73}",
      "tool_name": "read_file"
    }
  }
]

// 消息加载器类
export class MockMessageLoader {
  private rawMessages: MockRawMessage[];
  private currentIndex: number = 0;
  private intervalId: NodeJS.Timeout | null = null;
  private onMessageCallback: ((message: Message) => void) | null = null;

  constructor(messages: MockRawMessage[] = mockMessages) {
    this.rawMessages = messages;
  }

  private toMessage(raw: MockRawMessage, index: number): Message {
    return {
      id: `mock-${raw.timestamp}-${index}`,
      type: raw.type,
      data: raw.data,
      timestamp: raw.timestamp
    };
  }

  // 设置消息回调函数
  setOnMessage(callback: (message: Message) => void) {
    this.onMessageCallback = callback;
  }

  // 开始自动加载消息，每1秒一条 (加快速度)
  startAutoLoad() {
    if (this.intervalId) {
      this.stopAutoLoad();
    }

    this.intervalId = setInterval(() => {
      if (this.currentIndex < this.rawMessages.length) {
        const message = this.toMessage(this.rawMessages[this.currentIndex], this.currentIndex);
        if (this.onMessageCallback) {
          this.onMessageCallback(message);
        }
        this.currentIndex++;
      } else {
        // 所有消息加载完毕，停止自动加载
        this.stopAutoLoad();
      }
    }, 1000); // 每1秒加载一条消息
  }

  // 停止自动加载
  stopAutoLoad() {
    if (this.intervalId) {
      clearInterval(this.intervalId);
      this.intervalId = null;
    }
  }

  // 重置加载器
  reset() {
    this.stopAutoLoad();
    this.currentIndex = 0;
  }

  // 获取剩余消息数量
  getRemainingCount(): number {
    return this.rawMessages.length - this.currentIndex;
  }

  // 检查是否还有消息
  hasMoreMessages(): boolean {
    return this.currentIndex < this.rawMessages.length;
  }
}

// 导出单例实例
export const mockMessageLoader = new MockMessageLoader();
