import { useEffect, useRef, useState } from 'react';
import { ChevronDown } from 'lucide-react';

export interface InstanceOption {
  id: string;
  name: string;
  customersCount: number;
}

interface InstanceComboboxProps {
  instances: InstanceOption[];
  value: string;
  onChange: (id: string) => void;
  disabled?: boolean;
}

const displayLabel = (inst: InstanceOption) => `${inst.name} (${inst.customersCount} Customers)`;

// A searchable dropdown: with 56 benchmark instances a plain <select> is too
// long to scan, so this filters the option list as the user types instead.
export default function InstanceCombobox({ instances, value, onChange, disabled }: InstanceComboboxProps) {
  const [query, setQuery] = useState('');
  const [isOpen, setIsOpen] = useState(false);
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const containerRef = useRef<HTMLDivElement | null>(null);

  const selected = instances.find(i => i.id === value);
  const inputValue = isOpen ? query : (selected ? displayLabel(selected) : '');

  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setIsOpen(false);
      }
    }
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const filtered = instances.filter(inst => {
    const q = query.trim().toLowerCase();
    if (!q) return true;
    return inst.id.toLowerCase().includes(q) || inst.name.toLowerCase().includes(q);
  });

  const commit = (inst: InstanceOption) => {
    onChange(inst.id);
    setQuery('');
    setIsOpen(false);
  };

  return (
    <div className="relative" ref={containerRef}>
      <div className="relative">
        <input
          type="text"
          id="instance-select"
          disabled={disabled}
          value={inputValue}
          onChange={(e) => {
            setQuery(e.target.value);
            setIsOpen(true);
            setHighlightedIndex(0);
          }}
          onFocus={(e) => {
            setQuery('');
            setIsOpen(true);
            setHighlightedIndex(0);
            e.target.select();
          }}
          onKeyDown={(e) => {
            if (!isOpen) return;
            if (e.key === 'ArrowDown') {
              e.preventDefault();
              setHighlightedIndex(i => Math.min(i + 1, filtered.length - 1));
            } else if (e.key === 'ArrowUp') {
              e.preventDefault();
              setHighlightedIndex(i => Math.max(i - 1, 0));
            } else if (e.key === 'Enter') {
              e.preventDefault();
              const inst = filtered[highlightedIndex];
              if (inst) commit(inst);
            } else if (e.key === 'Escape') {
              setIsOpen(false);
              (e.target as HTMLInputElement).blur();
            }
          }}
          placeholder="Type to search instances..."
          autoComplete="off"
          role="combobox"
          aria-expanded={isOpen}
          aria-controls="instance-combobox-list"
          className="w-full text-sm border border-slate-300 rounded-lg p-2 pr-8 bg-white focus:border-blue-500 focus:ring-1 focus:ring-blue-500 disabled:opacity-50"
        />
        <ChevronDown className="h-4 w-4 text-slate-400 absolute right-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
      </div>
      {isOpen && (
        <div
          id="instance-combobox-list"
          role="listbox"
          className="absolute z-20 mt-1 w-full max-h-64 overflow-y-auto bg-white border border-slate-200 rounded-lg shadow-lg"
        >
          {filtered.length === 0 ? (
            <div className="px-3 py-2 text-sm text-slate-400">No matching instances</div>
          ) : (
            filtered.map((inst, idx) => (
              <button
                key={inst.id}
                type="button"
                role="option"
                aria-selected={inst.id === value}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => commit(inst)}
                onMouseEnter={() => setHighlightedIndex(idx)}
                className={`w-full text-left px-3 py-2 text-sm ${
                  idx === highlightedIndex ? 'bg-blue-50 text-blue-700' : 'text-slate-700'
                } ${inst.id === value ? 'font-semibold' : ''}`}
              >
                {displayLabel(inst)}
              </button>
            ))
          )}
        </div>
      )}
    </div>
  );
}
