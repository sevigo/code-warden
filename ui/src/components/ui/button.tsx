import * as React from "react"
import { Slot } from "@radix-ui/react-slot"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "@/lib/utils"

/**
 * HashiCorp-inspired Button Component
 * 
 * Key patterns from the design system:
 * - Primary dark: #15181e bg with #d5d7db text
 * - Secondary: white bg with #3b3d45 text
 * - Minimal border-radius (4-5px), never pill-shaped
 * - Asymmetric padding (more left padding for primary)
 * - Dual-layer micro-shadows at 0.05 opacity
 */

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap font-medium transition-all duration-150 focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        // HashiCorp Primary - Dark background (#15181e)
        primary: 
          "bg-[#15181e] text-[#d5d7db] border border-[rgba(178,182,189,0.4)] shadow-[0_1px_1px_rgba(97,104,117,0.05),0_2px_2px_rgba(97,104,117,0.05)] hover:bg-[#1e2025] active:bg-[#15181e]",
        
        // HashiCorp Secondary - White/light background
        secondary: 
          "bg-white text-[#3b3d45] border border-[#d5d7db] shadow-[0_1px_1px_rgba(97,104,117,0.05)] hover:bg-[#f1f2f3] active:bg-[#e1e3e6] dark:bg-[#1e2025] dark:text-[#d5d7db] dark:border-[#3b3d45] dark:hover:bg-[#2d2f36]",
        
        // Ghost - Minimal
        ghost: 
          "text-[#3b3d45] hover:bg-[#f1f2f3] active:bg-[#e1e3e6] dark:text-[#d5d7db] dark:hover:bg-[#2d2f36]",
        
        // Destructive - Error state
        destructive: 
          "bg-[#dc2626] text-white shadow-[0_1px_1px_rgba(97,104,117,0.05)] hover:bg-[#b91c1c] active:bg-[#991b1b]",
        
        // Product-specific colors
        "product-primary": 
          "bg-[#2264d6] text-white shadow-[0_1px_1px_rgba(97,104,117,0.05)] hover:bg-[#1060ff] active:bg-[#1d4ed8]",
        
        "product-secondary": 
          "bg-[#14c6cb] text-white shadow-[0_1px_1px_rgba(97,104,117,0.05)] hover:bg-[#12b6bb] active:bg-[#0fa5aa]",
        
        // Outline variant
        outline: 
          "bg-transparent text-[#3b3d45] border border-[#d5d7db] hover:bg-[#f1f2f3] hover:border-[#b2b6bd] active:bg-[#e1e3e6] dark:text-[#d5d7db] dark:border-[#3b3d45] dark:hover:bg-[#2d2f36]",
        
        // Link style
        link: 
          "text-[#2264d6] underline-offset-4 hover:underline dark:text-[#2b89ff]",
      },
      size: {
        // HashiCorp asymmetric padding: more left padding
        default: "h-10 px-4 py-2 text-sm",
        sm: "h-9 rounded-md px-3 text-xs",
        lg: "h-11 rounded-md px-6 text-sm",
        icon: "h-10 w-10 p-0",
        "icon-sm": "h-9 w-9 p-0",
        "icon-lg": "h-11 w-11 p-0",
      },
      radius: {
        default: "rounded-md",
        tight: "rounded-[4px]",
        standard: "rounded-[5px]",
        card: "rounded-[8px]",
      },
    },
    defaultVariants: {
      variant: "primary",
      size: "default",
      radius: "standard",
    },
  }
)

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean
  loading?: boolean
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, radius, asChild = false, loading = false, children, disabled, ...props }, ref) => {
    const Comp = asChild ? Slot : "button"
    
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, radius, className }))}
        ref={ref}
        disabled={disabled || loading}
        {...props}
      >
        {loading ? (
          <>
            <svg
              className="animate-spin h-4 w-4"
              xmlns="http://www.w3.org/2000/svg"
              fill="none"
              viewBox="0 0 24 24"
            >
              <circle
                className="opacity-25"
                cx="12"
                cy="12"
                r="10"
                stroke="currentColor"
                strokeWidth="4"
              />
              <path
                className="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
              />
            </svg>
            <span>{children}</span>
          </>
        ) : (
          children
        )}
      </Comp>
    )
  }
)
Button.displayName = "Button"

export { Button, buttonVariants }
