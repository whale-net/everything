"""Background worker that processes tasks."""
import time

def main():
    """Simple background worker."""
    print("Hello from background worker!")
    print("This worker processes tasks without exposing any services")
    print("Version: 1.0.0")
    
    # Simulate background processing
    print("Starting background processing loop...")
    for i in range(300):
        print(f"Processing batch {i+1}...")
        time.sleep(1)
    print("Worker completed processing")

if __name__ == "__main__":
    main()
