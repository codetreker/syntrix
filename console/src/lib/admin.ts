import { api } from './api'

// User type from backend (sensitive fields are redacted by server)
export interface User {
  id: string
  database: string
  username: string
  createdAt: string
  updatedAt: string
  disabled: boolean
  roles: string[]
  profile?: Record<string, unknown>
  last_login_at?: string
}

// RuleSet type matching backend identity/types
export interface MatchBlock {
  allow?: Record<string, string>
  match?: Record<string, MatchBlock>
}

export interface RuleSet {
  rules_version: string
  service: string
  match: Record<string, MatchBlock>
}

export interface UpdateUserRequest {
  roles: string[]
  db_admin?: string[]
  disabled: boolean
}

export interface DatabaseInfo {
  name: string
  [key: string]: unknown
}

// Database management types
export interface DatabaseSummary {
  id: string
  slug: string | null
  display_name: string
  owner_id: string
  created_at: string
  status: string
}

export interface DatabaseDetail {
  id: string
  slug: string | null
  display_name: string
  description?: string
  owner_id: string
  created_at: string
  updated_at: string
  status: string
  settings?: {
    max_documents: number
    max_storage_bytes: number
  }
}

export interface ListDatabasesResp {
  databases: DatabaseSummary[]
  total: number
}

export interface CreateDatabaseReq {
  slug?: string
  display_name: string
  description?: string
  owner_id?: string
  settings?: {
    max_documents?: number
    max_storage_bytes?: number
  }
}

export interface UpdateDatabaseReq {
  slug?: string
  display_name?: string
  description?: string
  status?: string
  settings?: {
    max_documents?: number
    max_storage_bytes?: number
  }
}

export interface DeleteDatabaseResp {
  id: string
  slug: string | null
  status: string
  message: string
}

export interface HealthInfo {
  status: string
  [key: string]: unknown
}

export const adminApi = {
  /**
   * List all users with optional pagination
   */
  async listUsers(limit = 50, offset = 0): Promise<User[]> {
    const response = await api.get<User[]>('/admin/users', {
      params: { limit, offset }
    })
    return response.data
  },

  /**
   * List all databases
   */
  async listDatabases(): Promise<ListDatabasesResp> {
    const response = await api.get<ListDatabasesResp>('/admin/databases')
    return response.data
  },

  /**
   * Get a single database by ID or slug
   */
  async getDatabase(identifier: string): Promise<DatabaseDetail> {
    const response = await api.get<DatabaseDetail>(`/admin/databases/${identifier}`)
    return response.data
  },

  /**
   * Create a new database
   */
  async createDatabase(data: CreateDatabaseReq): Promise<DatabaseDetail> {
    const response = await api.post<DatabaseDetail>('/admin/databases', data)
    return response.data
  },

  /**
   * Update a database
   */
  async updateDatabase(identifier: string, data: UpdateDatabaseReq): Promise<DatabaseDetail> {
    const response = await api.patch<DatabaseDetail>(`/admin/databases/${identifier}`, data)
    return response.data
  },

  /**
   * Delete a database
   */
  async deleteDatabase(identifier: string): Promise<DeleteDatabaseResp> {
    const response = await api.delete<DeleteDatabaseResp>(`/admin/databases/${identifier}`)
    return response.data
  },

  /**
   * Get admin health info
   */
  async adminHealth(): Promise<HealthInfo> {
    const response = await api.get<HealthInfo>('/admin/health')
    return response.data
  },

  /**
   * Update user roles and disabled status
   */
  async updateUser(id: string, data: UpdateUserRequest): Promise<void> {
    await api.patch(`/admin/users/${id}`, data)
  },

  /**
   * Get current authorization rules
   */
  async getRules(): Promise<RuleSet> {
    const response = await api.get<RuleSet>('/admin/rules')
    return response.data
  },

  /**
   * Push new authorization rules (YAML content)
   */
  async pushRules(content: string): Promise<void> {
    await api.post('/admin/rules/push', content, {
      headers: {
        'Content-Type': 'text/plain'
      }
    })
  },

  /**
   * Health check for admin endpoint
   */
  async health(): Promise<boolean> {
    try {
      await api.get('/admin/health')
      return true
    } catch {
      return false
    }
  }
}
