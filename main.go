package main

import (
    "fmt"
    "html/template"
    "io/ioutil"
    "net/http"
    "net/url"
    "os"
    "sync"
)

var templates = template.Must(template.ParseGlob("templates/*.html"))

// Hardcoded student and admin credentials
var studentUser = map[string]string{
    "student1": "1234",
}
var adminUser = map[string]string{
    "admin": "admin123",
}

// Hardcoded exams
var exams = []string{
    "Math Exam - Grade 10",
    "Science Exam - Grade 10",
}

// Store student results
type Result struct {
    Username string
    Score    int
}

// Store violation counts for each student
type Violation struct {
    Username string
    Count    int
}

var results []Result
var violations []Violation
var mu sync.Mutex

func main() {
    os.MkdirAll("captured_images", os.ModePerm)

    http.HandleFunc("/", loginPage)
    http.HandleFunc("/login", loginHandler)
    http.HandleFunc("/exam", examPage)
    http.HandleFunc("/proctor", proctorPage)
    http.HandleFunc("/capture", captureHandler)
    http.HandleFunc("/submit", submitHandler)
    http.HandleFunc("/score", scorePage)
    http.HandleFunc("/admin", adminPage)

    fmt.Println("Server running on http://localhost:8080")
    http.ListenAndServe(":8080", nil)
}

// Render login page
func loginPage(w http.ResponseWriter, r *http.Request) {
    templates.ExecuteTemplate(w, "login.html", nil)
}

// Handle login
func loginHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Redirect(w, r, "/", http.StatusSeeOther)
        return
    }
    username := r.FormValue("username")
    password := r.FormValue("password")

    if pass, ok := studentUser[username]; ok && pass == password {
        http.Redirect(w, r, "/exam?user="+username, http.StatusSeeOther)
        return
    }

    if pass, ok := adminUser[username]; ok && pass == password {
        http.Redirect(w, r, "/admin", http.StatusSeeOther)
        return
    }

    templates.ExecuteTemplate(w, "login.html", "Invalid credentials!")
}

// Render exam selection page
func examPage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    data := struct {
        Username string
        Exams    []string
    }{username, exams}
    templates.ExecuteTemplate(w, "exam.html", data)
}

// Render proctor page with username
func proctorPage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    templates.ExecuteTemplate(w, "proctor.html", map[string]string{"Username": username})
}

// Forward captured data to Python OpenCV service
func captureHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    imgData := r.FormValue("image")
    username := r.FormValue("username")
    noiseViolation := r.FormValue("noise_violation")

    resp, err := http.PostForm("http://localhost:5000/capture", url.Values{
        "image":           {imgData},
        "username":        {username},
        "noise_violation": {noiseViolation},
    })
    if err != nil {
        w.WriteHeader(http.StatusInternalServerError)
        w.Write([]byte("ERROR"))
        return
    }
    defer resp.Body.Close()
    body, _ := ioutil.ReadAll(resp.Body)
    
    // Check if response is a violation
    responseStr := string(body)
    if responseStr == "NOISE_VIOLATION" {
        // Increment violation count
        mu.Lock()
        found := false
        for i, v := range violations {
            if v.Username == username {
                violations[i].Count++
                found = true
                
                // Check if student has reached 10 violations
                if violations[i].Count >= 10 {
                    mu.Unlock()
                    w.Write([]byte("MAX_VIOLATIONS")) // Changed from "TERMINATE" to "MAX_VIOLATIONS"
                    return
                }
                
                // Return violation count to frontend
                w.Write([]byte(fmt.Sprintf("VIOLATION:%d", violations[i].Count)))
                mu.Unlock()
                return
            }
        }
        
        // If this is the first violation for this student
        if !found {
            violations = append(violations, Violation{Username: username, Count: 1})
            w.Write([]byte("VIOLATION:1"))
        }
        mu.Unlock()
        return
    }
    
    w.Write(body)
}

// Handle exam submission
func submitHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    username := r.FormValue("username")
    score := r.FormValue("score")

    var sc int
    fmt.Sscanf(score, "%d", &sc)

    mu.Lock()
    results = append(results, Result{Username: username, Score: sc})
    mu.Unlock()

    http.Redirect(w, r, "/score?user="+username, http.StatusSeeOther)
}

// Render student score page
func scorePage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    var studentScore int
    for _, res := range results {
        if res.Username == username {
            studentScore = res.Score
            break
        }
    }
    templates.ExecuteTemplate(w, "score.html", struct {
        Username string
        Score    int
    }{username, studentScore})
}

// Render admin page with all results
func adminPage(w http.ResponseWriter, r *http.Request) {
    mu.Lock()
    defer mu.Unlock()
    
    // Combine results and violations for admin view
    type AdminData struct {
        Results    []Result
        Violations []Violation
    }
    
    data := AdminData{
        Results:    results,
        Violations: violations,
    }
    
    templates.ExecuteTemplate(w, "admin.html", data)
}