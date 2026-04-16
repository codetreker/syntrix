import axios, { type AxiosInstance, type AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';

// API Base URL - defaults to same origin for production
const API_BASE_URL = import.meta.env.VITE_API_URL || '';

// Create axios instance
export const api: AxiosInstance = axios.create({
  baseURL: API_BASE_URL,
  headers: {
    'Content-Type': 'application/json',
  },
  timeout: 30000,
});

// Request interceptor to add auth token
api.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    const token = localStorage.getItem('token');
    if (token && config.headers) {
      config.headers.Authorization = `Bearer ${token}`;
    }
    return config;
  },
  (error: AxiosError) => {
    return Promise.reject(error);
  }
);

// Response interceptor to handle errors
api.interceptors.response.use(
  (response: AxiosResponse) => response,
  (error: AxiosError) => {
    if (error.response?.status === 401) {
      // Clear token from localStorage
      localStorage.removeItem('token');
      // Clear auth-storage (Zustand persist key)
      localStorage.removeItem('auth-storage');
      // Only redirect if not already on login page
      if (!window.location.pathname.includes('/login')) {
        window.location.href = '/console/login';
      }
    }
    return Promise.reject(error);
  }
);

// Auth API endpoints
export interface LoginRequest {
  username: string;
  password: string;
  database?: string;
}

export interface LoginResponse {
  access_token: string;
  refresh_token?: string;
  expires_in?: number;
}

export interface RefreshResponse {
  access_token: string;
  refresh_token?: string;
  expires_in?: number;
}

export interface SignupRequest {
  username: string;
  password: string;
  database?: string;
}

export const authApi = {
  login: async (data: LoginRequest): Promise<{ token: string; refreshToken?: string }> => {
    const response = await api.post<LoginResponse>('/auth/v1/login', {
      ...data,
      database: data.database || 'default',
    });
    // Backend returns access_token/refresh_token, normalize naming
    return {
      token: response.data.access_token,
      refreshToken: response.data.refresh_token,
    };
  },

  refresh: async (refreshToken: string): Promise<{ token: string; refreshToken?: string }> => {
    const response = await api.post<RefreshResponse>('/auth/v1/refresh', {
      refresh_token: refreshToken,
    });
    return {
      token: response.data.access_token,
      refreshToken: response.data.refresh_token,
    };
  },

  signup: async (data: SignupRequest): Promise<void> => {
    await api.post('/auth/v1/signup', {
      ...data,
      database: data.database || 'default',
    });
  },

  logout: async (refreshToken: string): Promise<void> => {
    await api.post('/auth/v1/logout', { refresh_token: refreshToken });
  },

  me: async () => {
    const response = await api.get('/auth/v1/me');
    return response.data;
  },
};

// Data API endpoints
export interface QueryRequest {
  collection: string;
  filter?: Record<string, unknown>;
  limit?: number;
  offset?: number;
}

export interface Document {
  id: string;
  [key: string]: unknown;
}

export const dataApi = {
  query: async (request: QueryRequest): Promise<Document[]> => {
    const response = await api.post('/api/v1/query', request);
    return response.data.documents || [];
  },

  create: async (collection: string, data: Record<string, unknown>): Promise<Document> => {
    const response = await api.post(`/api/v1/${collection}`, data);
    return response.data;
  },

  update: async (collection: string, id: string, data: Record<string, unknown>): Promise<Document> => {
    const response = await api.put(`/api/v1/${collection}/${id}`, data);
    return response.data;
  },

  delete: async (collection: string, id: string): Promise<void> => {
    await api.delete(`/api/v1/${collection}/${id}`);
  },

  get: async (collection: string, id: string): Promise<Document> => {
    const response = await api.get(`/api/v1/${collection}/${id}`);
    return response.data;
  },
};

// Auth-context User (minimal, for JWT-parsed current user)
export interface User {
  id: string;
  username: string;
  role: string;
  database: string;
  created_at?: string;
}

// Full admin API is in lib/admin.ts — do not duplicate here
