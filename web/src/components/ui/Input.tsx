import { forwardRef, type InputHTMLAttributes } from 'react';
import { cn } from '../../lib/utils';

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {}

const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ className, ...props }, ref) => {
    return (
      <input
        ref={ref}
        className={cn(
          'flex h-8 w-full rounded-sm border border-[#3c3c3c] bg-[#3c3c3c] px-3 py-1 text-sm text-[#cccccc] placeholder:text-[#858585] focus:outline-none focus:ring-1 focus:ring-[#007acc] focus:border-[#007acc] disabled:cursor-not-allowed disabled:opacity-50',
          className
        )}
        {...props}
      />
    );
  }
);

Input.displayName = 'Input';

export { Input };
