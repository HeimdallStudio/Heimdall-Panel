import { cn } from '@/lib/cn';

export function Logo({ className }: { className?: string }) {
  return (
    <span className={cn('inline-flex items-center font-semibold tracking-tight', className)}>
      Heimdall
    </span>
  );
}
