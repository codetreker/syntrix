import { Loader2 } from 'lucide-react';

export interface SpinnerProps {
  size?: 'sm' | 'md' | 'lg';
  className?: string;
}

const sizeClasses = {
  sm: 'w-4 h-4',
  md: 'w-6 h-6',
  lg: 'w-8 h-8',
};

export function Spinner({ size = 'md', className = '' }: SpinnerProps) {
  return <Loader2 className={`animate-spin ${sizeClasses[size]} ${className}`} />;
}

export interface LoadingOverlayProps {
  loading: boolean;
  message?: string;
}

export function LoadingOverlay({ loading, message }: LoadingOverlayProps) {
  if (!loading) return null;

  return (
    <div className="absolute inset-0 flex items-center justify-center bg-white/80 dark:bg-gray-900/80 z-10">
      <div className="flex flex-col items-center gap-2">
        <Spinner size="lg" />
        {message && <p className="text-sm text-gray-600 dark:text-gray-400">{message}</p>}
      </div>
    </div>
  );
}

export interface SkeletonProps {
  className?: string;
  variant?: 'text' | 'circular' | 'rectangular';
  width?: string | number;
  height?: string | number;
}

export function Skeleton({ className = '', variant = 'text', width, height }: SkeletonProps) {
  const baseClasses = 'bg-gray-200 dark:bg-gray-700 animate-pulse';
  
  const variantClasses = {
    text: 'rounded h-4',
    circular: 'rounded-full',
    rectangular: 'rounded',
  };

  const style: React.CSSProperties = {};
  if (width) style.width = typeof width === 'number' ? `${width}px` : width;
  if (height) style.height = typeof height === 'number' ? `${height}px` : height;

  return <div className={`${baseClasses} ${variantClasses[variant]} ${className}`} style={style} />;
}

// Page loading component
export function PageLoading() {
  return (
    <div className="flex items-center justify-center min-h-[400px]">
      <div className="flex flex-col items-center gap-3">
        <Spinner size="lg" className="text-blue-500" />
        <p className="text-gray-500 dark:text-gray-400">Loading...</p>
      </div>
    </div>
  );
}

// Skeleton for table rows
export function TableSkeleton({ rows = 5, columns = 4 }: { rows?: number; columns?: number }) {
  return (
    <div className="space-y-3">
      {Array.from({ length: rows }).map((_, rowIndex) => (
        <div key={rowIndex} className="flex gap-4">
          {Array.from({ length: columns }).map((_, colIndex) => (
            <Skeleton key={colIndex} className="flex-1" />
          ))}
        </div>
      ))}
    </div>
  );
}

// Skeleton for cards
export function CardSkeleton() {
  return (
    <div className="p-4 border border-gray-200 dark:border-gray-700 rounded-lg space-y-3">
      <Skeleton className="w-1/3 h-6" />
      <Skeleton className="w-full" />
      <Skeleton className="w-2/3" />
    </div>
  );
}
