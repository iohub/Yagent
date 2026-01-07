import { forwardRef, type ButtonHTMLAttributes } from 'react';
import { cn } from '../../lib/utils';

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'danger' | 'ghost';
  size?: 'sm' | 'md' | 'lg';
}

const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = 'primary', size = 'md', ...props }, ref) => {
    return (
      <button
        ref={ref}
        className={cn(
          'inline-flex items-center justify-center rounded-sm font-medium transition-colors focus:outline-none focus:ring-1 focus:ring-offset-0 disabled:opacity-50 disabled:pointer-events-none',
          {
            'bg-[#007acc] text-white hover:bg-[#0062a3] focus:ring-[#007acc]': variant === 'primary',
            'bg-[#3c3c3c] text-[#cccccc] hover:bg-[#4a4a4a] focus:ring-[#505050]': variant === 'secondary',
            'bg-red-700 text-white hover:bg-red-800 focus:ring-red-600': variant === 'danger',
            'bg-transparent text-[#cccccc] hover:bg-[#3c3c3c]': variant === 'ghost',
            'h-7 px-3 text-xs': size === 'sm',
            'h-8 px-4 py-1 text-sm': size === 'md',
            'h-10 px-6 text-base': size === 'lg',
          },
          className
        )}
        {...props}
      />
    );
  }
);

Button.displayName = 'Button';

export { Button };
