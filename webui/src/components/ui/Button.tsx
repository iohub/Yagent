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
            'bg-primary text-primary-foreground hover:bg-primary/90 focus:ring-ring': variant === 'primary',
            'bg-secondary text-secondary-foreground hover:bg-secondary/80 focus:ring-ring': variant === 'secondary',
            'bg-destructive text-destructive-foreground hover:bg-destructive/90 focus:ring-destructive': variant === 'danger',
            'bg-transparent hover:bg-accent hover:text-accent-foreground': variant === 'ghost',
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
